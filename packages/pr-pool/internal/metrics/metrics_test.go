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
	return &harness{reader: reader, emitter: emitter, q: q, clk: clk}
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
