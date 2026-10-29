package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// harness wires a ManualReader-backed meter provider to the catalog, mirroring
// pg-router's own internal/metrics test harness (metrics_test.go).
type harness struct {
	reader  *sdkmetric.ManualReader
	snap    Snapshot
	snapErr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h := &harness{reader: reader}
	err := New(mp, func() (Snapshot, error) { return h.snap, h.snapErr })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

// TestSourceAgeIsPerSourceAndOmitsUnknown pins pg_desk_source_age_seconds:
// one series per source with a known age, and NO series for an unknown one
// (the snapshot omits it), so a never-fetched source never reads as age 0.
func TestSourceAgeIsPerSourceAndOmitsUnknown(t *testing.T) {
	h := newHarness(t)
	h.snap = Snapshot{SourceAges: []SourceAge{
		{Source: "pg-connector-pr-github", Seconds: 120},
		{Source: "pg-connector-issue-jira", Seconds: 2400},
	}}
	g, ok := findMetric(t, h.collect(t), MetricSourceAge).Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatal("MetricSourceAge is not an int64 gauge")
	}
	got := map[string]int64{}
	for _, dp := range g.DataPoints {
		v, _ := dp.Attributes.Value(attribute.Key("source"))
		got[v.AsString()] = dp.Value
	}
	if len(got) != 2 || got["pg-connector-pr-github"] != 120 || got["pg-connector-issue-jira"] != 2400 {
		t.Fatalf("MetricSourceAge = %v, want one series per known source", got)
	}

	h.snap = Snapshot{}
	for _, sm := range h.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == MetricSourceAge && len(g.DataPoints) != 0 {
				t.Fatalf("MetricSourceAge exported %d series with no known source", len(g.DataPoints))
			}
		}
	}
}

// TestSnapshotComputedOncePerCollection guards bead pg2-jj0ym: the snapshot
// behind the gauges (a full BuildPayload plus store reads) MUST run once per
// scrape, not once per gauge. Nine redundant runs on a single-connection store
// pushed a /metrics scrape past 40s, beyond any scrape timeout.
func TestSnapshotComputedOncePerCollection(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	calls := 0
	if err := New(mp, func() (Snapshot, error) {
		calls++
		return Snapshot{AgeSeconds: 3, SourceAges: []SourceAge{{Source: "s", Seconds: 9}}}, nil
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
	var rm metricdata.ResourceMetrics
	for i := 1; i <= 3; i++ {
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect %d: %v", i, err)
		}
		if calls != i {
			t.Fatalf("after collection %d snapshotFn ran %d times, want %d (once per collection)", i, calls, i)
		}
	}
	if got := gaugeValue(t, findMetric(t, rm, MetricDashboardAge)); got != 3 {
		t.Fatalf("MetricDashboardAge = %d, want 3", got)
	}
}

// TestRemovedSyncEraMetricsNotEmitted is the plain negative case for the
// sync-era retirement: even when a collection runs, none of the retired
// pg_desk_sync_error*, anchor-check or reconcile series exists.
func TestRemovedSyncEraMetricsNotEmitted(t *testing.T) {
	h := newHarness(t)
	h.snap = Snapshot{AgeSeconds: 1, SourceAges: []SourceAge{{Source: "s", Seconds: 2}}}
	removed := []string{
		"pg_desk_sync_error_rows", "pg_desk_oldest_sync_error_age_seconds",
		"pg_desk_sync_error_retrying_rows", "pg_desk_sync_error_exhausted_rows",
		"pg_desk_sync_errors", "pg_desk_oldest_anchor_check_age_seconds",
	}
	for _, sm := range h.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if strings.HasPrefix(m.Name, "pg_desk_reconcile_") {
				t.Fatalf("retired metric %s still emitted", m.Name)
			}
			for _, r := range removed {
				if m.Name == r {
					t.Fatalf("retired metric %s still emitted", m.Name)
				}
			}
		}
	}
}
