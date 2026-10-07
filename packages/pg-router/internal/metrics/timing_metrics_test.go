package metrics

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// Tests for the queue-wait / run-time split and the oldest-age and per-listener
// in-flight gauges (bead pg2-n7da9).

// runningListener is an accepting listener with a settable id and bound type
// whose Offer advances the shared mock clock by run — a handler that "takes" run.
type runningListener struct {
	id, typ string
	clk     *mockClock
	run     time.Duration
}

func (l runningListener) ID() string                      { return l.id }
func (l runningListener) Matches(e eventqueue.Event) bool { return e.Type == l.typ }
func (l runningListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	l.clk.advance(l.run)
	return eventqueue.OfferResult{Accepted: true}
}

type histPoint struct {
	count uint64
	sum   float64
}

// histByRoleType flattens a float64 histogram to {role/type -> count,sum}.
func histByRoleType(t *testing.T, m metricdata.Metrics) map[string]histPoint {
	t.Helper()
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s is not a float64 histogram: %T", m.Name, m.Data)
	}
	out := map[string]histPoint{}
	for _, dp := range hist.DataPoints {
		role, _ := dp.Attributes.Value(attribute.Key("role"))
		typ, _ := dp.Attributes.Value(attribute.Key("type"))
		if got := dp.Attributes.Len(); got != 2 {
			t.Fatalf("%s datapoint has %d labels (%+v), want exactly role and type", m.Name, got, dp.Attributes)
		}
		out[role.AsString()+"/"+typ.AsString()] = histPoint{dp.Count, dp.Sum}
	}
	return out
}

func TestQueueWaitAndRunHistograms_SplitPerRoleAndFullEventType(t *testing.T) {
	h := newHarness(t)
	h.q.Register(runningListener{id: "desk-pr", typ: "pr.changed", clk: h.clk, run: 5 * time.Second})
	h.q.Register(runningListener{id: "desk-pr-reconcile", typ: "pr.reconcile", clk: h.clk, run: 20 * time.Second})

	// One Dispatch per event: the listeners share one mock clock, so offers that
	// advance it must not run concurrently within a pass.
	h.enqueue(t, "c1", "pr.changed", time.Hour)
	h.clk.advance(3 * time.Second) // waits 3s before being offered
	h.q.Dispatch()
	h.enqueue(t, "r1", "pr.reconcile", time.Hour)
	h.clk.advance(3 * time.Second)
	h.q.Dispatch()

	rm := h.collect(t)
	wait := findMetric(t, rm, MetricQueueWait)
	run := findMetric(t, rm, MetricRun)
	if wait.Unit != "s" || run.Unit != "s" {
		t.Fatalf("units = %q/%q, want s/s (exposed as _seconds)", wait.Unit, run.Unit)
	}
	gotWait := histByRoleType(t, wait)
	gotRun := histByRoleType(t, run)
	wantWait := map[string]histPoint{
		"desk-pr/pr.changed":             {1, 3},
		"desk-pr-reconcile/pr.reconcile": {1, 3},
	}
	wantRun := map[string]histPoint{
		"desk-pr/pr.changed":             {1, 5},
		"desk-pr-reconcile/pr.reconcile": {1, 20},
	}
	if !reflect.DeepEqual(gotWait, wantWait) {
		t.Fatalf("queue_wait = %+v, want %+v (pr.changed and pr.reconcile distinguishable)", gotWait, wantWait)
	}
	if !reflect.DeepEqual(gotRun, wantRun) {
		t.Fatalf("run = %+v, want %+v", gotRun, wantRun)
	}
	// Both share the documented bucket set.
	for _, m := range []metricdata.Metrics{wait, run} {
		hist := m.Data.(metricdata.Histogram[float64])
		if !reflect.DeepEqual(hist.DataPoints[0].Bounds, timingBuckets) {
			t.Fatalf("%s bounds = %v, want %v", m.Name, hist.DataPoints[0].Bounds, timingBuckets)
		}
	}
}

// The wait is measured against Event.At, which a source MAY stamp in the past, so
// a long-queued event reads as a long wait even though it was only just enqueued.
func TestQueueWaitMeasuredFromEventAt(t *testing.T) {
	h := newHarness(t)
	h.q.Register(runningListener{id: "desk-pr", typ: "pr.changed", clk: h.clk, run: time.Second})
	at := h.clk.now().Add(-90 * time.Second)
	if _, err := h.q.Enqueue(eventqueue.Event{ID: "old", Type: "pr.changed", At: at, ExpiresAt: h.clk.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	h.q.Dispatch()
	got := histByRoleType(t, findMetric(t, h.collect(t), MetricQueueWait))
	if got["desk-pr/pr.changed"].sum != 90 {
		t.Fatalf("queue_wait = %+v, want 90s measured from Event.At", got)
	}
}

func TestDeprecatedDispatchLatencyStillEmitted(t *testing.T) {
	h := newHarness(t)
	h.q.Register(runningListener{id: "desk-pr", typ: "pr.changed", clk: h.clk, run: 2 * time.Second})
	h.enqueue(t, "c1", "pr.changed", time.Hour)
	h.q.Dispatch()
	m := findMetric(t, h.collect(t), MetricDispatchLatency)
	hist := m.Data.(metricdata.Histogram[float64])
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
		t.Fatalf("legacy dispatch_latency = %+v, want its one datapoint still recorded", hist.DataPoints)
	}
	if want := "DEPRECATED"; len(m.Description) < len(want) || m.Description[:len(want)] != want {
		t.Fatalf("legacy description = %q, want it to start with DEPRECATED", m.Description)
	}
}

func TestEventTypeLabelBoundsCardinality(t *testing.T) {
	h := newHarness(t)
	for typ, want := range map[string]string{
		"pr.changed":                             "pr.changed",
		"issue.reconcile":                        "issue.reconcile",
		"bead":                                   "bead",
		"":                                       UnknownEntityType,
		"PR.changed":                             UnknownEntityType,
		".changed":                               UnknownEntityType,
		"has space":                              UnknownEntityType,
		"9starts-digit":                          UnknownEntityType,
		fmt.Sprintf("a%0*d", maxEventTypeLen, 1): UnknownEntityType, // too long
	} {
		if got := h.emitter.eventTypeLabel(typ); got != want {
			t.Errorf("eventTypeLabel(%q) = %q, want %q", typ, got, want)
		}
	}
	// A flood of distinct (valid) types mints at most maxEventTypeLabels series.
	fresh := newHarness(t)
	seen := map[string]bool{}
	for i := 0; i < maxEventTypeLabels+40; i++ {
		seen[fresh.emitter.eventTypeLabel(fmt.Sprintf("t%d.verb", i))] = true
	}
	if len(seen) != maxEventTypeLabels+1 { // the minted values plus the one fallback
		t.Fatalf("distinct label values = %d, want %d minted + 1 fallback", len(seen), maxEventTypeLabels)
	}
	if !seen[UnknownEntityType] {
		t.Fatalf("overflow types did not fold into %q", UnknownEntityType)
	}
}

func TestOnAcceptTimingNeedsNoCorrelationEntry(t *testing.T) {
	h := newHarness(t)
	base := h.clk.now()
	// No OnEnqueue was ever seen for this event (e.g. evicted from the FIFO map).
	h.emitter.OnAcceptTiming(eventqueue.DispatchTiming{
		EventID: "ghost", EventType: "pr.reconcile", ListenerID: "desk-pr",
		EnqueuedAt: base, StartedAt: base.Add(2 * time.Second), SettledAt: base.Add(12 * time.Second),
	})
	rm := h.collect(t)
	if got := histByRoleType(t, findMetric(t, rm, MetricQueueWait))["desk-pr/pr.reconcile"]; got.sum != 2 {
		t.Fatalf("queue_wait = %+v, want 2s", got)
	}
	if got := histByRoleType(t, findMetric(t, rm, MetricRun))["desk-pr/pr.reconcile"]; got.sum != 10 {
		t.Fatalf("run = %+v, want 10s", got)
	}
}

// gaugeValues flattens a gauge to {label value -> value} on key.
func gaugeValues(t *testing.T, m metricdata.Metrics, key string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	switch d := m.Data.(type) {
	case metricdata.Gauge[float64]:
		for _, dp := range d.DataPoints {
			v, _ := dp.Attributes.Value(attribute.Key(key))
			out[v.AsString()] = dp.Value
		}
	case metricdata.Gauge[int64]:
		for _, dp := range d.DataPoints {
			v, _ := dp.Attributes.Value(attribute.Key(key))
			out[v.AsString()] = float64(dp.Value)
		}
	default:
		t.Fatalf("%s is not a gauge: %T", m.Name, m.Data)
	}
	return out
}

func newGaugeHarness(t *testing.T) *harness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}
	var q *eventqueue.Queue
	emitter, err := New(
		mp, func() map[string]int { return q.DepthByType() },
		WithClock(clk.now),
		WithQueueAges(func() map[string]time.Duration { return q.OldestPendingAgeByType() }),
		WithListenerInFlight(func() map[string]int { return q.InFlightByListener() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	q, err = eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clk.now), eventqueue.WithObserver(emitter))
	if err != nil {
		t.Fatal(err)
	}
	return &harness{reader: reader, mp: mp, emitter: emitter, q: q, clk: clk}
}

func TestOldestAgeGaugePerTypeAndZeroWhenNothingOwed(t *testing.T) {
	h := newGaugeHarness(t)
	h.q.Register(runningListener{id: "desk-pr", typ: "pr.changed", clk: h.clk, run: time.Second})
	h.enqueue(t, "c1", "pr.changed", time.Hour)
	h.clk.advance(30 * time.Second)
	// A retained event no listener binds: counted in depth, but owed to nobody.
	h.enqueue(t, "u1", "other.type", time.Hour)

	m := findMetric(t, h.collect(t), MetricQueueOldestAge)
	if m.Unit != "s" {
		t.Fatalf("oldest age unit = %q, want s", m.Unit)
	}
	got := gaugeValues(t, m, "type")
	if want := map[string]float64{"pr.changed": 30, "other.type": 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("oldest age = %v, want %v", got, want)
	}

	h.q.Dispatch()
	got = gaugeValues(t, findMetric(t, h.collect(t), MetricQueueOldestAge), "type")
	if got["pr.changed"] != 0 {
		t.Fatalf("after dispatch oldest age[pr.changed] = %v, want 0", got["pr.changed"])
	}
}

func TestListenerInFlightGaugeOnePerRegisteredRole(t *testing.T) {
	h := newGaugeHarness(t)
	h.q.Register(runningListener{id: "desk-pr", typ: "pr.changed", clk: h.clk})
	h.q.Register(runningListener{id: "desk-issue", typ: "issue.changed", clk: h.clk})
	m := findMetric(t, h.collect(t), MetricListenerInFlight)
	got := gaugeValues(t, m, "role")
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"desk-issue", "desk-pr"}) || got["desk-pr"] != 0 || got["desk-issue"] != 0 {
		t.Fatalf("idle in-flight gauge = %v, want both roles at 0", got)
	}
}

func TestOptionalGaugesAbsentWithoutTheirOptions(t *testing.T) {
	h := newHarness(t) // no WithQueueAges / WithListenerInFlight
	h.q.Register(runningListener{id: "desk-pr", typ: "pr.changed", clk: h.clk})
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricQueueOldestAge || m.Name == MetricListenerInFlight {
				t.Fatalf("%s registered without its option", m.Name)
			}
		}
	}
}
