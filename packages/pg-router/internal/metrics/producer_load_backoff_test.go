package metrics

import (
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func loadBackoffGauge(t *testing.T, h *harness) int64 {
	t.Helper()
	m := findMetric(t, h.collect(t), MetricProducerLoadBackoff)
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 {
		t.Fatalf("%s = %#v, want one int64 gauge point", MetricProducerLoadBackoff, m.Data)
	}
	return g.DataPoints[0].Value
}

// The load-backoff gauge is 0 at start, 1 once a producer tick is skipped, and
// back to 0 on the next tick that ran; the counter counts only skipped ticks.
func TestOnProducerTickGaugeAndCounter(t *testing.T) {
	h := newHarness(t)
	if got := loadBackoffGauge(t, h); got != 0 {
		t.Fatalf("gauge at start = %d, want 0", got)
	}
	h.emitter.OnProducerTick(true)
	h.emitter.OnProducerTick(true)
	if got := loadBackoffGauge(t, h); got != 1 {
		t.Fatalf("gauge after skipped ticks = %d, want 1", got)
	}
	h.emitter.OnProducerTick(false)
	if got := loadBackoffGauge(t, h); got != 0 {
		t.Fatalf("gauge after a tick that ran = %d, want 0", got)
	}
	s, ok := findMetric(t, h.collect(t), MetricProducerTicksSkipped).Data.(metricdata.Sum[int64])
	if !ok || len(s.DataPoints) != 1 || s.DataPoints[0].Value != 2 {
		t.Fatalf("skipped-ticks counter = %#v, want 2 (the tick that ran is not counted)", s)
	}
}
