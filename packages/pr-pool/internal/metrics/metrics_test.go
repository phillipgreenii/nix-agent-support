package metrics

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// harness wires a ManualReader-backed meter provider to an Emitter and a real
// queue (the Emitter as the queue's Observer), so metrics are exercised
// end-to-end through the queue.
type harness struct {
	reader  *sdkmetric.ManualReader
	mp      metric.MeterProvider
	emitter *Emitter
	q       *eventqueue.Queue
	clk     *mockClock
}

type mockClock struct{ t time.Time }

func (c *mockClock) now() time.Time          { return c.t }
func (c *mockClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newHarness(t *testing.T) *harness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}

	var q *eventqueue.Queue
	emitter, err := New(mp, func() map[string]int { return q.DepthByType() })
	if err != nil {
		t.Fatalf("New emitter: %v", err)
	}
	q, err = eventqueue.New(eventqueue.NewMemStore(),
		eventqueue.WithClock(clk.now), eventqueue.WithObserver(emitter))
	if err != nil {
		t.Fatalf("New queue: %v", err)
	}
	return &harness{reader: reader, mp: mp, emitter: emitter, q: q, clk: clk}
}

func (h *harness) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

// findMetric returns the metric by name, or fails.
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

// sumFor returns the counter value for the given attribute key=value, or -1.
func sumFor(m metricdata.Metrics, key, value string) int64 {
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return -1
	}
	for _, dp := range s.DataPoints {
		if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
			return dp.Value
		}
	}
	return -1
}

// gaugeVal scans the whole collected set for the named gauge's datapoint with
// the given attribute, returning -1 when the metric or datapoint is absent (an
// observable gauge emits NO metric when its callback observes nothing, e.g. an
// empty queue — that absence means "zero").
func gaugeVal(rm metricdata.ResourceMetrics, name, key, value string) int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				return -1
			}
			for _, dp := range g.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
					return dp.Value
				}
			}
		}
	}
	return -1
}

// All three instruments register with the declared kind/unit and emit on the
// right event.
func TestInstrumentsRegistered(t *testing.T) {
	h := newHarness(t)
	// Drive one of each so the instruments appear in the collected set.
	h.emitter.RecordFailure("critical")
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	rm := h.collect(t)
	depth := findMetric(t, rm, MetricQueueDepth)
	if _, ok := depth.Data.(metricdata.Gauge[int64]); !ok {
		t.Fatalf("queue_depth is not a gauge: %T", depth.Data)
	}
	if depth.Unit != "{event}" {
		t.Fatalf("queue_depth unit = %q", depth.Unit)
	}
	failures := findMetric(t, rm, MetricFailures)
	if _, ok := failures.Data.(metricdata.Sum[int64]); !ok {
		t.Fatalf("failures is not a counter: %T", failures.Data)
	}
	// unconsumed_expired only appears once it has been incremented; assert its
	// registration indirectly through the dedicated expiry test below.
}

// TestCatalogHasTenMembers is Task 3.3's red-first test (register gaps
// R6/pg2-zqpxj, R21/pg2-00jpn, and pg2-cz31d): the catalog grows from 4 to 10
// members. WithLiveness is supplied here to prove MetricLiveness CAN
// register (the daemon-mode half of its binding decision) — see
// TestLivenessNotRegisteredWithoutOption below for the drain-mode half.
func TestCatalogHasTenMembers(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	var q *eventqueue.Queue
	emitter, err := New(mp, func() map[string]int { return q.DepthByType() }, WithLiveness(func() bool { return true }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q, err = eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithObserver(emitter))
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}

	// Drive one of each so every member has a recorded/observed data point —
	// an unincremented counter or an ObservableGauge whose callback never
	// calls Observe is ABSENT from Collect, not zero (see gaugeVal's doc).
	emitter.RecordFailure(FailureClassDeclined)
	emitter.RecordFailure(FailureClassDispatchFail)
	emitter.OnUnconsumedExpired("t")
	emitter.OnUnknownTypeRejected("t")
	emitter.RecordThroughput("t")
	emitter.OnSourceFailure("src")
	emitter.OnDeduped("t")
	emitter.RecordDispatchLatency(12.5)
	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "t", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	want := []string{
		MetricQueueDepth, MetricFailures, MetricUnconsumedExpired, MetricUnknownTypeRejected,
		MetricThroughput, MetricBacklog, MetricLiveness, MetricDispatchLatency,
		MetricSourceFailures, MetricDeduped,
	}
	if len(want) != 10 {
		t.Fatalf("test bug: want has %d entries, not 10", len(want))
	}
	for _, name := range want {
		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("catalog member %q did not register on the test MeterProvider", name)
		}
	}

	// RecordFailure accepts both classes, distinguished by the "class" label.
	m := findMetric(t, rm, MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 1 {
		t.Errorf("failures[%s] = %d, want 1", FailureClassDeclined, got)
	}
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 1 {
		t.Errorf("failures[%s] = %d, want 1", FailureClassDispatchFail, got)
	}
}

// MetricLiveness is registered ONLY when WithLiveness is supplied — the Task
// 3.3 binding decision's drain-mode half: drain-and-exit never registers this
// observable AT ALL, not merely never observes it live.
func TestLivenessNotRegisteredWithoutOption(t *testing.T) {
	h := newHarness(t) // no WithLiveness
	h.emitter.OnDeclined("t", "h", "busy")
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricLiveness {
				t.Fatalf("MetricLiveness registered without WithLiveness; want it absent entirely")
			}
		}
	}
}

// MetricLiveness reports 1 while isLive() is true, else 0 — a single scalar
// datapoint per collect, re-evaluated live on each callback invocation.
func TestLivenessReflectsIsLive(t *testing.T) {
	live := true
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(mp, func() map[string]int { return nil }, WithLiveness(func() bool { return live })); err != nil {
		t.Fatalf("New: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	m := findMetric(t, rm, MetricLiveness)
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 1 {
		t.Fatalf("liveness = %+v, want a single datapoint = 1 while isLive() is true", m.Data)
	}

	live = false
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	m = findMetric(t, rm, MetricLiveness)
	g, ok = m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 0 {
		t.Fatalf("liveness = %+v, want a single datapoint = 0 once isLive() is false", m.Data)
	}
}

// MetricBacklog is a scalar sum(DepthByType()) — distinct from the existing
// per-type MetricQueueDepth gauge.
func TestBacklogIsScalarSum(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.enqueue(t, "e2", "push-requested", 10*time.Minute)
	rm := h.collect(t)
	m := findMetric(t, rm, MetricBacklog)
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 2 {
		t.Fatalf("backlog = %+v, want a single scalar datapoint = 2 (sum across types)", m.Data)
	}
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got != 1 {
		t.Fatalf("queue_depth[review-requested] = %d, want 1 (per-type, unaffected by backlog's scalar)", got)
	}
}

// OnSourceFailure (Task 3.3, register gap R21 / bead pg2-00jpn, INV-FAIL-3)
// increments the source-failures counter, per source.
func TestOnSourceFailurePerSource(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnSourceFailure("github-pulls")
	h.emitter.OnSourceFailure("github-pulls")
	h.emitter.OnSourceFailure("jira-issues")
	m := findMetric(t, h.collect(t), MetricSourceFailures)
	if got := sumFor(m, "source", "github-pulls"); got != 2 {
		t.Fatalf("source_failures[github-pulls] = %d, want 2", got)
	}
	if got := sumFor(m, "source", "jira-issues"); got != 1 {
		t.Fatalf("source_failures[jira-issues] = %d, want 1", got)
	}
}

// OnDeduped (INV-EVT-3, bead pg2-cz31d) increments the deduped counter, per
// type.
func TestOnDedupedPerType(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeduped("review-requested")
	h.emitter.OnDeduped("review-requested")
	h.emitter.OnDeduped("push-requested")
	m := findMetric(t, h.collect(t), MetricDeduped)
	if got := sumFor(m, "type", "review-requested"); got != 2 {
		t.Fatalf("deduped[review-requested] = %d, want 2", got)
	}
	if got := sumFor(m, "type", "push-requested"); got != 1 {
		t.Fatalf("deduped[push-requested] = %d, want 1", got)
	}
}

// RecordThroughput increments the throughput counter, per type. Exported for
// direct/test use — see its doc for why no production call site feeds it yet.
func TestRecordThroughputPerType(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordThroughput("review-requested")
	h.emitter.RecordThroughput("review-requested")
	m := findMetric(t, h.collect(t), MetricThroughput)
	if got := sumFor(m, "type", "review-requested"); got != 2 {
		t.Fatalf("throughput[review-requested] = %d, want 2", got)
	}
}

// MetricDispatchLatency is the catalog's one Histogram, registered with
// WithUnit("ms") and the exact explicit bucket boundaries the Task 3.3
// binding decision specifies.
func TestRecordDispatchLatency_HistogramWithBuckets(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordDispatchLatency(42)
	m := findMetric(t, h.collect(t), MetricDispatchLatency)
	if m.Unit != "ms" {
		t.Fatalf("dispatch_latency unit = %q, want ms", m.Unit)
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("dispatch_latency is not a histogram: %T", m.Data)
	}
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
		t.Fatalf("dispatch_latency datapoints = %+v, want exactly one recorded value", hist.DataPoints)
	}
	wantBounds := []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}
	if !reflect.DeepEqual(hist.DataPoints[0].Bounds, wantBounds) {
		t.Fatalf("dispatch_latency bucket bounds = %v, want %v", hist.DataPoints[0].Bounds, wantBounds)
	}
}

// RecordFailure accepts both FailureClassDeclined and FailureClassDispatchFail
// — see FailureClassDispatchFail's own doc for its production call site
// (eventqueue.Observer.OnDispatchFailure).
func TestRecordFailure_AcceptsBothClasses(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordFailure(FailureClassDeclined)
	h.emitter.RecordFailure(FailureClassDispatchFail)
	h.emitter.RecordFailure(FailureClassDispatchFail)
	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 1 {
		t.Fatalf("failures[%s] = %d, want 1", FailureClassDeclined, got)
	}
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 2 {
		t.Fatalf("failures[%s] = %d, want 2", FailureClassDispatchFail, got)
	}
}

// queue depth tracks enqueue/accept: it reflects the live retained set per type.
func TestQueueDepthTracksEnqueue(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.enqueue(t, "e2", "review-requested", 10*time.Minute)
	rm := h.collect(t)
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got != 2 {
		t.Fatalf("queue_depth[review-requested] = %d, want 2", got)
	}
	// Once expired AND unowed the depth returns to zero (gauge emits no datapoint -> -1).
	h.clk.advance(11 * time.Minute)
	h.q.Expire()
	rm = h.collect(t)
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got > 0 {
		t.Fatalf("queue_depth after expiry = %d, want 0 (or absent)", got)
	}
}

// Integration through the queue: unconsumed-expired fires when an event expires
// with no accepting consumer (per-type label).
func TestUnconsumedExpiredThroughQueue(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "orphan1", "review-requested", 5*time.Minute)
	h.clk.advance(6 * time.Minute)
	h.q.Expire()
	rm := h.collect(t)
	if got := sumFor(findMetric(t, rm, MetricUnconsumedExpired), "type", "review-requested"); got != 1 {
		t.Fatalf("unconsumed_expired[review-requested] = %d, want 1", got)
	}
}

// failure rate increments per failure class.
func TestFailureRatePerClass(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordFailure("resource-limit")
	h.emitter.RecordFailure("resource-limit")
	h.emitter.RecordFailure("critical")
	rm := h.collect(t)
	m := findMetric(t, rm, MetricFailures)
	if got := sumFor(m, "class", "resource-limit"); got != 2 {
		t.Fatalf("failures[resource-limit] = %d, want 2", got)
	}
	if got := sumFor(m, "class", "critical"); got != 1 {
		t.Fatalf("failures[critical] = %d, want 1", got)
	}
}

// The Emitter is the core's ingest observer too, so the ingest-time condition
// INV-DISP-3 requires in metrics can actually be wired to it (core.Options.Observer).
// The assertion lives in the test so the production import DAG keeps pointing one
// way — core never depends on metrics, and metrics never depends on core.
var _ core.IngestObserver = (*Emitter)(nil)

// The Emitter is discover's pull-source-failure observer too (Task 3.3,
// register gap R21 / bead pg2-00jpn). Same posture as core.IngestObserver
// above: the assertion lives in the test, not in metrics.go, so metrics.go's
// own production import list stays exactly what it needs and no more.
var _ discover.SourceFailureObserver = (*Emitter)(nil)

// unknown-type-rejected increments per event type: the metric half of INV-DISP-3's
// "the condition is recorded to logs and metrics".
func TestUnknownTypeRejectedPerType(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnUnknownTypeRejected("review-abandoned")
	h.emitter.OnUnknownTypeRejected("review-abandoned")
	h.emitter.OnUnknownTypeRejected("never-declared")

	m := findMetric(t, h.collect(t), MetricUnknownTypeRejected)
	if _, ok := m.Data.(metricdata.Sum[int64]); !ok {
		t.Fatalf("unknown_type_rejected is not a counter: %T", m.Data)
	}
	if m.Unit != "{event}" {
		t.Fatalf("unknown_type_rejected unit = %q, want {event}", m.Unit)
	}
	if got := sumFor(m, "type", "review-abandoned"); got != 2 {
		t.Fatalf("unknown_type_rejected[review-abandoned] = %d, want 2", got)
	}
	if got := sumFor(m, "type", "never-declared"); got != 1 {
		t.Fatalf("unknown_type_rejected[never-declared] = %d, want 1", got)
	}
}

// The no-op observer hooks are safe to call (queue drives them).
func TestNoopHooks(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnEnqueue(eventqueue.Event{})
	h.emitter.OnAccept("e", "l")
}

// OnDeclined — the queue's pre-accept-decline / dispatch-failure signal
// (eventqueue.Observer, INV-FAIL-1) — feeds the SAME pr_pool.failures counter
// RecordFailure does, labeled with the one class knowable at that call site.
// Task 2.3 widened the signature to 3 args (evtType, listenerID, reason);
// none of the three is part of the failure-rate label set.
func TestOnDeclinedFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeclined("review-requested", "h1", "busy")
	h.emitter.OnDeclined("review-requested", "h1", "busy")
	h.emitter.OnDeclined("push-requested", "h2", "unavailable")

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 3 {
		t.Fatalf("failures[%s] = %d, want 3 (evtType/listenerID/reason are not part of the label set)", FailureClassDeclined, got)
	}
}

// OnDeduped is Task 2.3's new eventqueue.Observer method. Task 3.3 (landed
// independently, ahead of this task on main) already promoted MetricDeduped
// to a real OTel catalog member fed from core.IngestObserver's OnDeduped —
// the SAME Emitter method eventqueue.Observer's OnDeduped now also satisfies
// (both interfaces name an identical `OnDeduped(evtType string)`), so this
// proves it increments that real counter rather than a second, redundant
// in-process one.
func TestEmitter_OnDedupedIncrementsCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeduped("review-requested")
	h.emitter.OnDeduped("review-requested")

	m := findMetric(t, h.collect(t), MetricDeduped)
	if got := sumFor(m, "type", "review-requested"); got != 2 {
		t.Fatalf("deduped[%s] = %d, want 2", "review-requested", got)
	}
}

// Integration through the queue: a real pre-accept decline in Dispatch reaches
// the failures counter via OnDeclined — proving the production call site, not
// just the method in isolation.
func TestDeclineThroughQueueFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	l := &decliningListener{typ: "review-requested"}
	h.q.Register(l)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	h.q.Dispatch() // pre-accept decline: l always declines

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 1 {
		t.Fatalf("failures[%s] = %d, want 1 after one Dispatch-path decline", FailureClassDeclined, got)
	}
}

// OnDispatchFailure — the queue's OTHER delivery-side failure signal
// (eventqueue.Observer, INV-OBS-1) — feeds the SAME pr_pool.failures counter
// RecordFailure does, labeled with FailureClassDispatchFail.
func TestOnDispatchFailureFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDispatchFailure("review-requested")
	h.emitter.OnDispatchFailure("review-requested")
	h.emitter.OnDispatchFailure("push-requested")

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 3 {
		t.Fatalf("failures[%s] = %d, want 3 (evtType is not part of the label set)", FailureClassDispatchFail, got)
	}
}

// Integration through the queue: a real dispatch failure (a listener's Offer
// panicking, recovered by eventqueue.Queue's offerSafely) reaches the
// failures counter via OnDispatchFailure — proving the production call site
// bead pg2-icm3u adds, not just the method in isolation, and proving it lands
// under the OTHER class from a graceful decline (TestDeclineThroughQueue...
// above).
func TestDispatchFailureThroughQueueFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	l := &panickingListener{typ: "review-requested"}
	h.q.Register(l)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	h.q.Dispatch() // Offer panics; offerSafely recovers it as a dispatch failure

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 1 {
		t.Fatalf("failures[%s] = %d, want 1 after one Dispatch-path panic", FailureClassDispatchFail, got)
	}
	if got := sumFor(m, "class", FailureClassDeclined); got != -1 {
		t.Fatalf("failures[%s] = %d, want none recorded — a panic is not a graceful decline", FailureClassDeclined, got)
	}
}

// decliningListener is a minimal eventqueue.Listener that always declines
// (pre-accept, busy) events of its bound type — enough to drive Dispatch's
// Offer()==false branch without pulling in eventqueue's own test doubles
// (unexported to that package).
type decliningListener struct{ typ string }

func (decliningListener) ID() string { return "declining" }
func (l decliningListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (decliningListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy}
}

// acceptingListener is a minimal eventqueue.Listener that always ACCEPTS
// events of its bound type — a real accept path (as opposed to
// decliningListener's decline) for exercising DepthByType/Flush together.
type acceptingListener struct{ typ string }

func (acceptingListener) ID() string { return "accepting" }
func (l acceptingListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (acceptingListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

// panickingListener is a minimal eventqueue.Listener whose Offer always
// PANICS for events of its bound type — bead pg2-icm3u's dispatch-failure
// path: eventqueue.Queue's offerSafely recovers the panic and Dispatch
// reports it via OnDispatchFailure rather than OnDeclined (decliningListener
// above stays the graceful-decline test double).
type panickingListener struct{ typ string }

func (panickingListener) ID() string { return "panicking" }
func (l panickingListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (panickingListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	panic("panickingListener: simulated Offer panic (dispatch failure)")
}

// New surfaces an instrument-registration error rather than panicking.
func TestNewInstrumentError(t *testing.T) {
	if _, err := New(failMP{}, func() map[string]int { return nil }); err == nil {
		t.Fatal("expected instrument-registration error")
	}
}

// failMP is a MeterProvider whose meter fails to create a counter — exercises
// New's error path. It embeds the noop bases to satisfy OTel's sealed
// interfaces, overriding only what must fail.
type failMP struct{ noop.MeterProvider }

func (failMP) Meter(string, ...metric.MeterOption) metric.Meter { return failMeter{} }

type failMeter struct{ noop.Meter }

func (failMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errors.New("instrument boom")
}

// Flush against the no-op MeterProvider default (INV-OBS-1: core stays
// unaware of any concrete backend) must be a safe no-op, never an error — the
// no-op provider implements no ForceFlush at all.
func TestFlushNoopProviderIsSafe(t *testing.T) {
	if err := Flush(context.Background(), noop.NewMeterProvider()); err != nil {
		t.Fatalf("Flush(noop provider) = %v, want nil", err)
	}
}

// Flush against a REAL MeterProvider forces its reader to collect immediately
// rather than waiting for a periodic tick — the acceptance criterion this
// helper exists for: "scrape after a short run reports non-empty depth."
// sdkmetric.NewManualReader has no periodic tick of its own, so this proves
// ForceFlush is actually invoked (a stub Flush that silently did nothing would
// still pass a bare "Collect works" test, but this drives it through the SAME
// harness/queue path run-until-idle uses, immediately before the collect that
// stands in for the scrape).
func TestFlushRealProviderReportsNonEmptyDepth(t *testing.T) {
	h := newHarness(t)
	h.q.Register(acceptingListener{typ: "review-requested"})
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.q.Dispatch() // accepted; still RETAINED (and so still counted) until it expires+sweeps

	if err := Flush(context.Background(), h.mp); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rm := h.collect(t)
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got != 1 {
		t.Fatalf("queue_depth[review-requested] after Flush = %d, want 1 (non-empty depth reported)", got)
	}
}

// NewReadableProvider's MeterProvider must actually back live instruments —
// an Emitter constructed on it and driven records a value the paired
// Reader's Snapshot can read back (Task 3.6-prereq's value-read-back
// acceptance criterion). Unlike newHarness's own reader (which the TEST
// constructs and owns directly), this proves the PRODUCTION constructor
// bootCore is expected to call wires the two together correctly on its own.
func TestNewReadableProvider_SnapshotReadsBackRecordedValue(t *testing.T) {
	mp, reader := NewReadableProvider()
	emitter, err := New(mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatalf("New emitter: %v", err)
	}

	emitter.OnUnconsumedExpired("review-requested")

	rm, err := reader.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	m := findMetric(t, rm, MetricUnconsumedExpired)
	if got := sumFor(m, "type", "review-requested"); got != 1 {
		t.Fatalf("%s{type=review-requested} = %d, want 1", MetricUnconsumedExpired, got)
	}
}

// A Reader with nothing recorded yet must still Snapshot cleanly (no
// instruments driven at all is not an error condition — the same "absence
// means zero" posture gaugeVal's own doc already states for an observable
// gauge).
func TestNewReadableProvider_SnapshotBeforeAnyRecordingIsNotAnError(t *testing.T) {
	mp, reader := NewReadableProvider()
	if _, err := New(mp, func() map[string]int { return nil }); err != nil {
		t.Fatalf("New emitter: %v", err)
	}

	if _, err := reader.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot before any recording = %v, want nil", err)
	}
}

// --- helpers --------------------------------------------------------------

// enqueue appends an event with an explicit absolute `expiresAt`, computed off the
// harness's mock clock. Expiry is an INSTANT and never a duration (DEC-EVENT-1),
// so `expiresIn` is a test-side convenience for picking that instant, not a field
// the event carries.
func (h *harness) enqueue(t *testing.T, id, typ string, expiresIn time.Duration) {
	t.Helper()
	evt := eventqueue.Event{ID: id, Type: typ, ExpiresAt: h.clk.now().Add(expiresIn)}
	if _, err := h.q.Enqueue(evt); err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}
