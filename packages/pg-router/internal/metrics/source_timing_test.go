package metrics

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Per-source attempt duration histogram and in-flight children gauge (bead
// pg2-zdowv, DEC-OBS-8).

func newTimingHarness(t *testing.T, sources map[string]time.Duration) (*Emitter, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	e, err := New(mp, func() map[string]int { return nil }, WithPullSources(sources))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e, reader
}

func collectMetric(t *testing.T, r *sdkmetric.ManualReader, name string) (metricdata.Metrics, bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

func inflight(t *testing.T, r *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	m, ok := collectMetric(t, r, MetricSourceInflightChildren)
	if !ok {
		t.Fatalf("%s not exported", MetricSourceInflightChildren)
	}
	g := m.Data.(metricdata.Gauge[int64])
	out := map[string]int64{}
	for _, dp := range g.DataPoints {
		v, _ := dp.Attributes.Value(attribute.Key("source"))
		out[v.AsString()] = dp.Value
	}
	return out
}

func TestSourceInflight_SeededAtZeroThenTracksStartAndEnd(t *testing.T) {
	e, r := newTimingHarness(t, map[string]time.Duration{"pr-mine": time.Minute, "no-period": 0})
	got := inflight(t, r)
	if v, ok := got["pr-mine"]; !ok || v != 0 {
		t.Fatalf("seeded series = %v, want pr-mine=0 from start", got)
	}
	if _, ok := got["no-period"]; ok {
		t.Fatalf("a source without a period must not be seeded: %v", got)
	}

	e.OnSourceAttemptStart("pr-mine")
	if got := inflight(t, r)["pr-mine"]; got != 1 {
		t.Fatalf("inflight during an attempt = %d, want 1", got)
	}
	e.OnSourceAttemptEnd("pr-mine", 2*time.Second, false)
	if got := inflight(t, r)["pr-mine"]; got != 0 {
		t.Fatalf("inflight after the attempt = %d, want 0", got)
	}
	// An unmatched end never drives the gauge negative.
	e.OnSourceAttemptEnd("pr-mine", time.Second, false)
	if got := inflight(t, r)["pr-mine"]; got != 0 {
		t.Fatalf("inflight after a stray end = %d, want 0", got)
	}
}

func TestSourceDuration_RecordsPerSourceSecondsExceptShutdown(t *testing.T) {
	e, r := newTimingHarness(t, map[string]time.Duration{"pr-mine": time.Minute})
	e.OnSourceAttemptStart("pr-mine")
	e.OnSourceAttemptEnd("pr-mine", 30*time.Second, false)
	e.OnSourceAttemptStart("pr-mine")
	e.OnSourceAttemptEnd("pr-mine", 500*time.Millisecond, false)
	e.OnSourceAttemptStart("pr-mine")
	e.OnSourceAttemptEnd("pr-mine", 3*time.Second, true) // cut short by shutdown: not a sample

	m, ok := collectMetric(t, r, MetricSourceDuration)
	if !ok {
		t.Fatalf("%s not exported", MetricSourceDuration)
	}
	if m.Unit != "s" {
		t.Errorf("unit = %q, want s (so the exposition suffix is _seconds)", m.Unit)
	}
	h := m.Data.(metricdata.Histogram[float64])
	if len(h.DataPoints) != 1 {
		t.Fatalf("datapoints = %d, want 1 (label is source only)", len(h.DataPoints))
	}
	dp := h.DataPoints[0]
	if v, _ := dp.Attributes.Value(attribute.Key("source")); v.AsString() != "pr-mine" {
		t.Errorf("source label = %q", v.AsString())
	}
	if dp.Count != 2 || dp.Sum != 30.5 {
		t.Errorf("count/sum = %d/%v, want 2 samples summing 30.5 (the shutdown attempt excluded)", dp.Count, dp.Sum)
	}
}
