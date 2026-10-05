package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Per-source last-success timestamp and expected-interval gauges (bead
// pg2-tv11a, DEC-OBS-4).

type sourceLivenessHarness struct {
	emitter *Emitter
	reader  *sdkmetric.ManualReader
	clk     *mockClock
}

func newSourceLivenessHarness(t *testing.T, sources map[string]time.Duration) *sourceLivenessHarness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	e, err := New(mp, func() map[string]int { return nil }, WithClock(clk.now), WithPullSources(sources))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &sourceLivenessHarness{emitter: e, reader: reader, clk: clk}
}

// floatGauge returns every source->value pair of the named float gauge.
func (h *sourceLivenessHarness) floatGauge(t *testing.T, name string) map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[float64])
			if !ok {
				t.Fatalf("%s is %T, want a float64 gauge", name, m.Data)
			}
			if m.Unit != "s" {
				t.Errorf("%s unit = %q, want s (so the exposition suffix is _seconds)", name, m.Unit)
			}
			for _, dp := range g.DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key("source"))
				out[v.AsString()] = dp.Value
			}
		}
	}
	return out
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func TestSourceLiveness_InitialisedToProcessStartNotZero(t *testing.T) {
	h := newSourceLivenessHarness(t, map[string]time.Duration{"pr-team": time.Minute, "thread-me": 30 * time.Minute})
	got := h.floatGauge(t, MetricSourceLastSuccess)
	for _, src := range []string{"pr-team", "thread-me"} {
		if got[src] != unix(h.clk.t) {
			t.Errorf("last-success{%s} = %v, want process start %v (never 0)", src, got[src], unix(h.clk.t))
		}
	}
	iv := h.floatGauge(t, MetricSourceExpectedInterval)
	if iv["pr-team"] != 60 || iv["thread-me"] != 1800 {
		t.Errorf("expected-interval = %v, want pr-team 60 and thread-me 1800 seconds", iv)
	}
}

func TestSourceLiveness_AdvancesOnSuccessAndPauseButNotFailure(t *testing.T) {
	h := newSourceLivenessHarness(t, map[string]time.Duration{"a": time.Minute, "b": time.Minute, "c": time.Minute})
	start := h.clk.t

	h.clk.advance(10 * time.Minute)
	h.emitter.OnSourceSucceeded("a")
	h.emitter.OnSourceFailure("b", errors.New("boom"))
	h.emitter.OnSourcePaused("c")

	got := h.floatGauge(t, MetricSourceLastSuccess)
	if got["a"] != unix(h.clk.t) {
		t.Errorf("a (succeeded) = %v, want advanced to %v", got["a"], unix(h.clk.t))
	}
	if got["b"] != unix(start) {
		t.Errorf("b (failed) = %v, want untouched at start %v: a failure must not advance the gauge", got["b"], unix(start))
	}
	if got["c"] != unix(h.clk.t) {
		t.Errorf("c (paused by a gate or halt) = %v, want advanced to %v: a pause is not a failure", got["c"], unix(h.clk.t))
	}
}

// Only the sources handed to WithPullSources are exported: a disabled,
// excluded or push source is simply never passed, and a late notification for
// an unknown name must not mint a new series.
func TestSourceLiveness_OnlyRegisteredSourcesAreExported(t *testing.T) {
	h := newSourceLivenessHarness(t, map[string]time.Duration{"pulled": time.Minute, "no-period": 0})
	h.emitter.OnSourceSucceeded("stranger")
	h.emitter.OnSourcePaused("stranger")

	for _, name := range []string{MetricSourceLastSuccess, MetricSourceExpectedInterval} {
		got := h.floatGauge(t, name)
		if len(got) != 1 {
			t.Errorf("%s series = %v, want only the registered pull source with a period", name, got)
		}
		if _, ok := got["pulled"]; !ok {
			t.Errorf("%s missing the registered source: %v", name, got)
		}
	}
}

func TestSourceLiveness_NotRegisteredWithoutOption(t *testing.T) {
	h := newHarness(t)
	// The no-op hooks must be safe when no gauge exists.
	h.emitter.OnSourceSucceeded("x")
	h.emitter.OnSourcePaused("x")
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricSourceLastSuccess || m.Name == MetricSourceExpectedInterval {
				t.Errorf("%s registered without WithPullSources", m.Name)
			}
		}
	}
}
