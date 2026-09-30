package metrics

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestScanWorktreePool_CountsDirsAndSumsBytes(t *testing.T) {
	root := t.TempDir()
	for bead, size := range map[string]int{"pg2-a": 10, "pg2-b": 5} {
		d := filepath.Join(root, bead, "sub")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file at the top level is not a worktree.
	if err := os.WriteFile(filepath.Join(root, "stray"), []byte("xxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := scanWorktreePool(context.Background(), root)
	if got.count != 2 || got.bytes != 15 || got.truncated {
		t.Errorf("scan = %+v, want count=2 bytes=15 truncated=false", got)
	}
}

func TestScanWorktreePool_MissingDirIsEmpty(t *testing.T) {
	got := scanWorktreePool(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if got != (poolStat{}) {
		t.Errorf("scan = %+v, want zero", got)
	}
}

func TestScanWorktreePool_CancelledContextTruncates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "b", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := scanWorktreePool(ctx, root)
	if got.count != 1 || !got.truncated {
		t.Errorf("scan = %+v, want count=1 truncated=true", got)
	}
}

// TestPoolScanner_TTLCachesAndNeverBlocks: the first get() reports not-ready
// and starts one background scan; within the TTL further gets never rescan;
// after the TTL a get() returns the stale value and schedules a refresh.
func TestPoolScanner_TTLCachesAndNeverBlocks(t *testing.T) {
	clk := &mockClock{t: time.Unix(1000, 0)}
	var scans atomic.Int32
	done := make(chan struct{}, 4)
	p := newPoolScanner("/x", time.Minute, clk.now)
	p.scan = func(context.Context, string) poolStat {
		n := scans.Add(1)
		defer func() { done <- struct{}{} }()
		return poolStat{count: int64(n)}
	}
	if _, ok := p.get(); ok {
		t.Fatal("first get: ok=true before any scan completed")
	}
	<-done
	// Wait for refresh to store its result.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, ok := p.get(); ok {
			if st.count != 1 {
				t.Fatalf("count = %d, want 1", st.count)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan result never cached")
		}
		time.Sleep(time.Millisecond)
	}
	p.get()
	p.get()
	if n := scans.Load(); n != 1 {
		t.Errorf("scans within TTL = %d, want 1", n)
	}
	clk.advance(2 * time.Minute)
	st, ok := p.get() // stale: served from cache, refresh kicked
	if !ok || st.count != 1 {
		t.Errorf("stale get = %+v ok=%v, want cached count=1", st, ok)
	}
	<-done
	if n := scans.Load(); n != 2 {
		t.Errorf("scans after TTL = %d, want 2", n)
	}
}

func TestWithWorktreePool_RegistersGauges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pg2-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pg2-a", "f"), make([]byte, 7), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	if _, err := New(mp, func() map[string]int { return nil }, WithWorktreePool(root, time.Hour)); err != nil {
		t.Fatalf("New: %v", err)
	}
	collect := func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if g, ok := m.Data.(metricdata.Gauge[int64]); ok && len(g.DataPoints) == 1 {
					out[m.Name] = g.DataPoints[0].Value
				}
			}
		}
		return out
	}
	collect() // kicks the background scan
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := collect()
		if v, ok := got[MetricWorktreeCount]; ok {
			if v != 1 || got[MetricWorktreeBytes] != 7 || got[MetricWorktreeScanTruncated] != 0 {
				t.Fatalf("gauges = %v, want count=1 bytes=7 truncated=0", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worktree gauges never appeared")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNew_WithoutWorktreePool_RegistersNoPoolGauges(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	if _, err := New(mp, func() map[string]int { return nil }); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricWorktreeCount {
				t.Errorf("%s registered without WithWorktreePool", m.Name)
			}
		}
	}
}
