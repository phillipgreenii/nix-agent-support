package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

type groupFixture struct {
	t      *testing.T
	tel    *Telemetry
	reader *sdkmetric.ManualReader
	store  string
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	f := &groupFixture{t: t, reader: sdkmetric.NewManualReader()}
	f.store = filepath.Join(t.TempDir(), "store.sqlite")
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	var err error
	f.tel, err = New(Options{MeterProvider: mp, TracerProvider: tracenoop.NewTracerProvider(), StorePath: f.store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.tel.Close() })
	return f
}

func (f *groupFixture) collect() map[string]metricdata.Metrics {
	f.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := f.reader.Collect(context.Background(), &rm); err != nil {
		f.t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func sumByLabels(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: %T is not an int sum", m.Name, m.Data)
	}
	out := map[string]int64{}
	for _, p := range s.DataPoints {
		out[setString(p.Attributes)] = p.Value
	}
	return out
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGroupObserverCountsRevalidationsByMappedResult(t *testing.T) {
	f := newGroupFixture(t)
	o := NewGroupObserver(f.tel)
	o.Revalidated("detail", "extended")
	o.Revalidated("detail", "repulled")
	o.Revalidated("detail", "max_age")

	got := sumByLabels(t, f.collect()[MetricRevalidation])
	want := map[string]int64{
		"group=detail,result=extended":          1,
		"group=detail,result=repulled":          1,
		"group=detail,result=forced_by_max_age": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("revalidation = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("revalidation[%s] = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestGroupObserverRecordsStalenessInSeconds(t *testing.T) {
	f := newGroupFixture(t)
	o := NewGroupObserver(f.tel)
	o.Served("detail", 0)
	o.Served("conversation", 90*time.Second)

	h, ok := f.collect()[MetricGroupStaleness].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("group.staleness_seconds is not a float histogram")
	}
	got := map[string]float64{}
	for _, p := range h.DataPoints {
		if p.Count != 1 {
			t.Errorf("%s: count = %d, want 1", setString(p.Attributes), p.Count)
		}
		got[setString(p.Attributes)] = p.Sum
	}
	if len(got) != 2 || got["group=detail"] != 0 || got["group=conversation"] != 90 {
		t.Errorf("staleness points = %v, want detail 0 and conversation 90", got)
	}
}

func TestRecordEvictionsIncrementsTheCounter(t *testing.T) {
	f := newGroupFixture(t)
	f.tel.RecordEvictions(3)
	f.tel.RecordEvictions(0)
	f.tel.RecordEvictions(-2)
	got := sumByLabels(t, f.collect()[MetricStoreEvictions])
	if got[""] != 3 || len(got) != 1 {
		t.Errorf("store.evictions = %v, want 3 with no labels", got)
	}
}

func TestStoreBytesSumsTheStoreFileItsWalAndShm(t *testing.T) {
	f := newGroupFixture(t)
	read := func() int64 {
		g, ok := f.collect()[MetricStoreBytes].Data.(metricdata.Gauge[int64])
		if !ok || len(g.DataPoints) != 1 {
			t.Fatalf("store.bytes = %v", f.collect()[MetricStoreBytes].Data)
		}
		return g.DataPoints[0].Value
	}
	if got := read(); got != 0 {
		t.Errorf("store.bytes with no files = %d, want 0", got)
	}
	writeFile(t, f.store, 100)
	writeFile(t, f.store+"-wal", 30)
	writeFile(t, f.store+"-shm", 7)
	if got := read(); got != 137 {
		t.Errorf("store.bytes = %d, want 100+30+7 = 137", got)
	}
	if err := os.Remove(f.store + "-wal"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != 107 {
		t.Errorf("store.bytes after removing the WAL = %d, want 107", got)
	}
}

func TestStoreBytesIsUnreportedWithoutAStorePath(t *testing.T) {
	f := newFixture(t)
	if _, ok := f.collect()[MetricStoreBytes]; ok {
		t.Error("store.bytes reported although no store path was supplied")
	}
}

func TestGroupMetricsCarryOnlyGroupAndResultLabels(t *testing.T) {
	f := newGroupFixture(t)
	o := NewGroupObserver(f.tel)
	// An id or an invented word in a group or result position must fold away.
	o.Revalidated("example-owner/example-repo#41", "adhoc-result-77")
	o.Served("PR_kwDOexample", time.Second)
	o.Revalidated("runs", "extended")
	o.Served("files", 5*time.Second)
	f.tel.RecordEvictions(1)
	writeFile(t, f.store, 1)

	keys := map[string]bool{}
	var values []string
	for name, m := range f.collect() {
		if !strings.HasPrefix(name, "pg_connector_github.revalidation") &&
			name != MetricGroupStaleness && name != MetricStoreBytes && name != MetricStoreEvictions {
			continue
		}
		for _, set := range labelSets(t, m) {
			if set == "" {
				continue
			}
			for _, kv := range strings.Split(set, ",") {
				k, v, _ := strings.Cut(kv, "=")
				keys[k] = true
				values = append(values, v)
			}
		}
	}
	if len(keys) != 2 || !keys["group"] || !keys["result"] {
		t.Errorf("attribute keys = %v, want exactly group and result", keys)
	}
	for _, v := range values {
		if strings.ContainsAny(v, "#/") || strings.Contains(v, "example") || strings.Contains(v, "adhoc") || strings.Contains(v, "PR_") {
			t.Errorf("label value %q looks like an id or an ad-hoc key", v)
		}
	}
}

func TestGroupObserverIsInertWithNoopProviders(t *testing.T) {
	tel, err := New(Options{
		MeterProvider:  metricnoop.NewMeterProvider(),
		TracerProvider: tracenoop.NewTracerProvider(),
		StorePath:      filepath.Join(t.TempDir(), "store.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tel.Close() }()
	o := NewGroupObserver(tel)
	o.Revalidated("detail", "extended")
	o.Served("detail", time.Minute)
	tel.RecordEvictions(2)
}

func TestSetupWithoutEndpointKeepsTheGroupObserverInert(t *testing.T) {
	shutdown, err := Setup(Config{})
	if err != nil {
		t.Fatal(err)
	}
	tel, err := New(Options{StorePath: filepath.Join(t.TempDir(), "store.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	o := NewGroupObserver(tel)
	o.Revalidated("detail", "repulled")
	o.Served("pending", 0)
	tel.RecordEvictions(1)
	if err := tel.Close(); err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
