package metrics

import (
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// --- Gate Registry metrics (bead pg2-h63eu) ---------------------------------

// gateHarness wires an Emitter as BOTH the queue's Observer and its
// GateObserver, with the active-gates gauge reading the queue.
func newGateHarness(t *testing.T) *harness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var q *eventqueue.Queue
	emitter, err := New(mp, func() map[string]int { return q.DepthByType() }, WithClock(clk.now),
		WithActiveGates(func() []eventqueue.Gate { return q.ActiveGates() }))
	if err != nil {
		t.Fatalf("New emitter: %v", err)
	}
	q, err = eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clk.now),
		eventqueue.WithObserver(emitter), eventqueue.WithGateObserver(emitter))
	if err != nil {
		t.Fatalf("New queue: %v", err)
	}
	return &harness{reader: reader, mp: mp, emitter: emitter, q: q, clk: clk}
}

func TestGateMetrics_ActiveGaugeAndSetClearExpireCounters(t *testing.T) {
	h := newGateHarness(t)
	if _, err := h.q.SetGate(eventqueue.GateRequest{Type: "ALPHA", Owner: "somebody"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.SetGate(eventqueue.GateRequest{Type: "ALPHA", Owner: "somebody"}); err != nil { // renewal
		t.Fatal(err)
	}
	if _, err := h.q.SetGate(eventqueue.GateRequest{Type: "BETA", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}

	rm := h.collect(t)
	if got := gaugeVal(rm, MetricActiveGates, "type", "ALPHA"); got != 1 {
		t.Errorf("active gauge ALPHA = %d, want 1", got)
	}
	if got := gaugeVal(rm, MetricActiveGates, "type", "BETA"); got != 1 {
		t.Errorf("active gauge BETA = %d, want 1", got)
	}
	sets := findMetric(t, rm, MetricGateSets)
	if got := sumForBoth(sets, "type", "ALPHA", "renewal", "false"); got != 1 {
		t.Errorf("sets{ALPHA,renewal=false} = %d, want 1", got)
	}
	if got := sumForBoth(sets, "type", "ALPHA", "renewal", "true"); got != 1 {
		t.Errorf("sets{ALPHA,renewal=true} = %d, want 1 (each renewal is another GateSet)", got)
	}

	// Clear ALPHA after 90s; let BETA's lease lapse.
	h.clk.advance(90 * time.Second)
	if _, _, err := h.q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	h.q.Expire()
	rm = h.collect(t)
	if got := gaugeVal(rm, MetricActiveGates, "type", "ALPHA"); got != -1 {
		t.Errorf("active gauge ALPHA = %d after clear, want absent", got)
	}
	if got := gaugeVal(rm, MetricActiveGates, "type", "BETA"); got != -1 {
		t.Errorf("active gauge BETA = %d after its lease lapsed, want absent", got)
	}
	if got := sumFor(findMetric(t, rm, MetricGateClears), "type", "ALPHA"); got != 1 {
		t.Errorf("clears{ALPHA} = %d, want 1", got)
	}
	if got := sumFor(findMetric(t, rm, MetricGateExpiries), "type", "BETA"); got != 1 {
		t.Errorf("expiries{BETA} = %d, want 1", got)
	}
	dur := findMetric(t, rm, MetricGateDuration)
	hist, ok := dur.Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) != 2 {
		t.Fatalf("gate duration = %T %+v, want a histogram with a cleared and an expired series", dur.Data, dur.Data)
	}
	for _, dp := range hist.DataPoints {
		outcome, _ := dp.Attributes.Value("outcome")
		typ, _ := dp.Attributes.Value("type")
		switch {
		case typ.AsString() == "ALPHA" && outcome.AsString() == "cleared":
			if dp.Sum != 90 {
				t.Errorf("ALPHA held %vs, want 90", dp.Sum)
			}
		case typ.AsString() == "BETA" && outcome.AsString() == "expired":
			if dp.Sum != 60 {
				t.Errorf("BETA held %vs, want 60 (its lease)", dp.Sum)
			}
		default:
			t.Errorf("unexpected duration series type=%s outcome=%s", typ.AsString(), outcome.AsString())
		}
	}
}

// Blocked dispatches, blocked polls, rejected pulls and gate-caused drops are
// counted per participant and TYPE — and the gate OWNER is never a label.
func TestGateMetrics_BlockedAndDropsPerParticipantAndType_OwnerIsNeverALabel(t *testing.T) {
	h := newGateHarness(t)
	l := namedAcceptingListener{id: "worker", typ: "work"}
	h.q.Register(l)
	if _, err := h.q.SetGate(eventqueue.GateRequest{Type: "ALPHA", Owner: "high-cardinality-owner-7f3a"}); err != nil {
		t.Fatal(err)
	}
	// An unexpired event waits (a blocked DISPATCH); a born-expired one is
	// dropped at its final attempt (a gate-caused DROP).
	h.enqueue(t, "waits", "work", time.Hour)
	if _, err := h.q.Enqueue(eventqueue.Event{ID: "dies", Type: "work"}); err != nil {
		t.Fatal(err)
	}
	h.q.Dispatch()
	_ = h.q.CheckPull("worker", nil)
	_, _ = h.q.EmitterBlockedBy("src", nil)

	rm := h.collect(t)
	blocked := findMetric(t, rm, MetricGateBlocked)
	if got := sumForBoth(blocked, "kind", "dispatch", "participant", "worker"); got < 1 {
		t.Errorf("blocked{dispatch,worker} = %d, want >= 1", got)
	}
	if got := sumForBoth(blocked, "kind", "pull", "participant", "worker"); got != 1 {
		t.Errorf("blocked{pull,worker} = %d, want 1", got)
	}
	if got := sumForBoth(blocked, "kind", "poll", "participant", "src"); got != 1 {
		t.Errorf("blocked{poll,src} = %d, want 1", got)
	}
	drops := findMetric(t, rm, MetricGateDrops)
	if got := sumForBoth(drops, "role", "worker", "gate", "ALPHA"); got != 1 {
		t.Errorf("drops{worker,ALPHA} = %d, want 1", got)
	}
	// Cardinality guard: the owner string must appear in NO metric's attributes.
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					assertNoOwnerLabel(t, m.Name, dp.Attributes.ToSlice())
				}
			case metricdata.Gauge[int64]:
				for _, dp := range d.DataPoints {
					assertNoOwnerLabel(t, m.Name, dp.Attributes.ToSlice())
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					assertNoOwnerLabel(t, m.Name, dp.Attributes.ToSlice())
				}
			}
		}
	}
}

// An arbitrary TYPE vocabulary cannot blow up the series count: past
// maxGateTypeLabels distinct TYPEs, the rest share one OTHER label.
func TestGateMetrics_TypeCardinalityIsCapped(t *testing.T) {
	h := newGateHarness(t)
	for i := 0; i < maxGateTypeLabels+10; i++ {
		h.emitter.OnGateSet(fmt.Sprintf("TYPE_%d", i), false)
	}
	rm := h.collect(t)
	sets := findMetric(t, rm, MetricGateSets)
	s, _ := sets.Data.(metricdata.Sum[int64])
	if len(s.DataPoints) != maxGateTypeLabels+1 {
		t.Fatalf("series = %d, want %d TYPEs + the OTHER bucket", len(s.DataPoints), maxGateTypeLabels+1)
	}
	if got := sumFor(sets, "type", GateLabelOther); got != 10 {
		t.Errorf("OTHER bucket = %d, want the 10 overflow TYPEs", got)
	}
	// A TYPE seen before the cap keeps its own label.
	h.emitter.OnGateSet("TYPE_0", false)
	if got := sumFor(findMetric(t, h.collect(t), MetricGateSets), "type", "TYPE_0"); got != 2 {
		t.Errorf("TYPE_0 = %d, want 2", got)
	}
}

func assertNoOwnerLabel(t *testing.T, metric string, attrs []attribute.KeyValue) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == "owner" || a.Value.AsString() == "high-cardinality-owner-7f3a" {
			t.Errorf("metric %s carries the gate owner as a label (%s=%s): owner is DEBUG ONLY and unbounded", metric, a.Key, a.Value.AsString())
		}
	}
}

// The queue-log size gauge (bead pg2-8e0m6) reads the supplied size on each
// collect, and is not registered at all without WithQueueLogSize.
func TestQueueLogBytesGauge(t *testing.T) {
	size := int64(1234)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(mp, func() map[string]int { return nil }, WithQueueLogSize(func() int64 { return size })); err != nil {
		t.Fatal(err)
	}
	h := &harness{reader: reader, mp: mp}
	read := func() int64 {
		m := findMetric(t, h.collect(t), MetricQueueLogBytes)
		g, ok := m.Data.(metricdata.Gauge[int64])
		if !ok || len(g.DataPoints) != 1 {
			t.Fatalf("%s data = %#v, want one int64 gauge point", MetricQueueLogBytes, m.Data)
		}
		return g.DataPoints[0].Value
	}
	if got := read(); got != 1234 {
		t.Fatalf("gauge = %d, want 1234", got)
	}
	size = 56
	if got := read(); got != 56 {
		t.Fatalf("gauge after the log shrank = %d, want 56", got)
	}

	// Not registered without the option.
	reader2 := sdkmetric.NewManualReader()
	mp2 := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader2))
	if _, err := New(mp2, func() map[string]int { return nil }); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader2.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricQueueLogBytes {
				t.Fatal("queue log gauge registered without WithQueueLogSize")
			}
		}
	}
}
