package metrics

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// harness wires a ManualReader-backed meter provider to an Emitter, mirroring
// pg-router's own internal/metrics test harness (metrics_test.go).
type harness struct {
	reader  *sdkmetric.ManualReader
	emitter *Emitter
	snap    Snapshot
	snapErr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h := &harness{reader: reader}
	emitter, err := New(mp, func() (Snapshot, error) { return h.snap, h.snapErr })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.emitter = emitter
	return h
}

func (h *harness) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not found in collected data", name)
	return metricdata.Metrics{}
}

func gaugeValue(t *testing.T, m metricdata.Metrics) int64 {
	t.Helper()
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 {
		t.Fatalf("%s: want exactly one int64 gauge data point, got %#v", m.Name, m.Data)
	}
	return g.DataPoints[0].Value
}

// TestLivenessAlwaysOne is the "1 while pg-desk serve answers this scrape"
// contract: reaching the callback at all already proves liveness.
func TestLivenessAlwaysOne(t *testing.T) {
	h := newHarness(t)
	rm := h.collect(t)
	if got := gaugeValue(t, findMetric(t, rm, MetricLiveness)); got != 1 {
		t.Fatalf("MetricLiveness = %d, want 1", got)
	}
}

// TestDashboardStalePolarity_FreshIsZero is the acceptance criterion's
// explicit polarity check: a fresh payload MUST read 0, not 1 — the
// opposite of a presence-style gauge.
func TestDashboardStalePolarity_FreshIsZero(t *testing.T) {
	h := newHarness(t)
	h.snap = Snapshot{AgeSeconds: 5, Stale: false, DroppedCount: 0}
	rm := h.collect(t)
	if got := gaugeValue(t, findMetric(t, rm, MetricDashboardStale)); got != 0 {
		t.Fatalf("MetricDashboardStale (fresh) = %d, want 0 (0 = fresh)", got)
	}
}

// TestDashboardStalePolarity_StaleIsOne is the other half of the same
// acceptance criterion: a stale payload MUST read 1.
func TestDashboardStalePolarity_StaleIsOne(t *testing.T) {
	h := newHarness(t)
	h.snap = Snapshot{AgeSeconds: 999, Stale: true, DroppedCount: 0}
	rm := h.collect(t)
	if got := gaugeValue(t, findMetric(t, rm, MetricDashboardStale)); got != 1 {
		t.Fatalf("MetricDashboardStale (stale) = %d, want 1 (1 = stale)", got)
	}
}

func TestDashboardAgeSeconds(t *testing.T) {
	h := newHarness(t)
	h.snap = Snapshot{AgeSeconds: 42}
	rm := h.collect(t)
	if got := gaugeValue(t, findMetric(t, rm, MetricDashboardAge)); got != 42 {
		t.Fatalf("MetricDashboardAge = %d, want 42", got)
	}
}

func TestDropped(t *testing.T) {
	h := newHarness(t)
	h.snap = Snapshot{DroppedCount: 7}
	rm := h.collect(t)
	if got := gaugeValue(t, findMetric(t, rm, MetricDropped)); got != 7 {
		t.Fatalf("MetricDropped = %d, want 7", got)
	}
}

// TestRecordSyncError proves the counter accumulates per repo and is
// registered as a genuine monotonic Sum, even though no production call
// site is wired (MetricSyncErrors' own doc).
func TestRecordSyncError(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordSyncError("acme/widgets")
	h.emitter.RecordSyncError("acme/widgets")
	h.emitter.RecordSyncError("other/repo")
	rm := h.collect(t)
	m := findMetric(t, rm, MetricSyncErrors)
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("MetricSyncErrors: want Sum[int64], got %#v", m.Data)
	}
	if !s.IsMonotonic {
		t.Fatalf("MetricSyncErrors: want a monotonic counter")
	}
	got := map[string]int64{}
	for _, dp := range s.DataPoints {
		repo, _ := dp.Attributes.Value(attribute.Key("repo"))
		got[repo.AsString()] = dp.Value
	}
	if got["acme/widgets"] != 2 {
		t.Fatalf("MetricSyncErrors[acme/widgets] = %d, want 2", got["acme/widgets"])
	}
	if got["other/repo"] != 1 {
		t.Fatalf("MetricSyncErrors[other/repo] = %d, want 1", got["other/repo"])
	}
}

// TestSnapshotFuncErrorPropagates confirms a store-read failure surfaces
// through Collect rather than being silently swallowed.
func TestSnapshotFuncErrorPropagates(t *testing.T) {
	h := newHarness(t)
	h.snapErr = errors.New("boom")
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err == nil {
		t.Fatal("Collect: want an error when snapshotFn fails")
	}
}
