package metrics

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// The log-size limit's gauges (bead pg2-5d3ui) read the supplied status on each
// collect; the soft step shows up as pg_router_emitters_halted and NOT as a gate.
func TestLogLimitGauges(t *testing.T) {
	st := eventqueue.LimitStatus{Bytes: 500, SoftBytes: 900, HardBytes: 1000, State: eventqueue.StateOK}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(mp, func() map[string]int { return nil }, WithLogLimitStatus(func() eventqueue.LimitStatus { return st })); err != nil {
		t.Fatal(err)
	}
	h := &harness{reader: reader, mp: mp}

	intGauge := func(name string) map[string]int64 {
		m := findMetric(t, h.collect(t), name)
		g, ok := m.Data.(metricdata.Gauge[int64])
		if !ok {
			t.Fatalf("%s data = %#v, want an int64 gauge", name, m.Data)
		}
		out := map[string]int64{}
		for _, dp := range g.DataPoints {
			v, _ := dp.Attributes.Value(attribute.Key("reason"))
			out[v.AsString()] = dp.Value
		}
		return out
	}
	floatGauge := func(name string) float64 {
		m := findMetric(t, h.collect(t), name)
		g, ok := m.Data.(metricdata.Gauge[float64])
		if !ok || len(g.DataPoints) != 1 {
			t.Fatalf("%s data = %#v, want one float64 gauge point", name, m.Data)
		}
		return g.DataPoints[0].Value
	}

	if got := intGauge(MetricQueueLogLimitBytes)[""]; got != 1000 {
		t.Errorf("limit gauge = %d, want 1000", got)
	}
	if got := floatGauge(MetricQueueLogPercent); got != 50 {
		t.Errorf("percent gauge = %v, want 50", got)
	}
	if got := intGauge(MetricEmittersHalted)[""]; got != 0 {
		t.Errorf("emitters_halted = %d, want 0 when ok", got)
	}
	if got := intGauge(MetricLogRejecting); got["log_full"] != 0 || got["log_unwritable"] != 0 || len(got) != 2 {
		t.Errorf("log_rejecting = %v, want both reasons present at 0", got)
	}

	st = eventqueue.LimitStatus{Bytes: 950, SoftBytes: 900, HardBytes: 1000, State: eventqueue.StateEmittersHalted, EmittersHalted: true}
	if got := intGauge(MetricEmittersHalted)[""]; got != 1 {
		t.Errorf("emitters_halted = %d, want 1 when soft-halted", got)
	}
	if got := intGauge(MetricLogRejecting); got["log_full"] != 0 || got["log_unwritable"] != 0 {
		t.Errorf("log_rejecting = %v: a soft halt rejects nothing", got)
	}

	st = eventqueue.LimitStatus{Bytes: 1200, SoftBytes: 900, HardBytes: 1000, State: eventqueue.StateLogFull, EmittersHalted: true}
	if got := floatGauge(MetricQueueLogPercent); got != 120 {
		t.Errorf("percent gauge = %v, want 120", got)
	}
	if got := intGauge(MetricLogRejecting); got["log_full"] != 1 || got["log_unwritable"] != 0 {
		t.Errorf("log_rejecting = %v, want log_full=1", got)
	}

	st = eventqueue.LimitStatus{State: eventqueue.StateLogUnwritable, EmittersHalted: true}
	if got := intGauge(MetricLogRejecting); got["log_unwritable"] != 1 || got["log_full"] != 0 {
		t.Errorf("log_rejecting = %v, want log_unwritable=1", got)
	}
}

func TestLogLimitGaugesNotRegisteredWithoutOption(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(mp, func() map[string]int { return nil }); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case MetricQueueLogLimitBytes, MetricQueueLogPercent, MetricEmittersHalted, MetricLogRejecting:
				t.Fatalf("%s registered without WithLogLimitStatus", m.Name)
			}
		}
	}
}

// OnEnqueueRejected counts per type and reason.
func TestOnEnqueueRejectedCounts(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnEnqueueRejected("a.ready", eventqueue.ReasonLogFull)
	h.emitter.OnEnqueueRejected("a.ready", eventqueue.ReasonLogFull)
	h.emitter.OnEnqueueRejected("b.ready", eventqueue.ReasonLogUnwritable)
	m := findMetric(t, h.collect(t), MetricEnqueueRejected)
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("data = %#v", m.Data)
	}
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		ty, _ := dp.Attributes.Value("type")
		re, _ := dp.Attributes.Value("reason")
		got[ty.AsString()+"/"+re.AsString()] = dp.Value
	}
	if got["a.ready/log_full"] != 2 || got["b.ready/log_unwritable"] != 1 || len(got) != 2 {
		t.Fatalf("counts = %v", got)
	}
}
