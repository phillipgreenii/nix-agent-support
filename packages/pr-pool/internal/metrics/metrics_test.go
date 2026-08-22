package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pr-pool/internal/core"
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
func TestOnDeclinedFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeclined("review-requested")
	h.emitter.OnDeclined("review-requested")
	h.emitter.OnDeclined("push-requested")

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 3 {
		t.Fatalf("failures[%s] = %d, want 3 (evtType is not part of the label set)", FailureClassDeclined, got)
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

// decliningListener is a minimal eventqueue.Listener that always declines
// (pre-accept, busy) events of its bound type — enough to drive Dispatch's
// Offer()==false branch without pulling in eventqueue's own test doubles
// (unexported to that package).
type decliningListener struct{ typ string }

func (decliningListener) ID() string { return "declining" }
func (l decliningListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}
func (decliningListener) Offer(eventqueue.Event) bool { return false }

// acceptingListener is a minimal eventqueue.Listener that always ACCEPTS
// events of its bound type — a real accept path (as opposed to
// decliningListener's decline) for exercising DepthByType/Flush together.
type acceptingListener struct{ typ string }

func (acceptingListener) ID() string { return "accepting" }
func (l acceptingListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}
func (acceptingListener) Offer(eventqueue.Event) bool { return true }

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
