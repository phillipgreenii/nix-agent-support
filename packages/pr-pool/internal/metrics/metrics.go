// Package metrics emits these members of the core's declared metric catalog
// (INV-OBS-1), each named by INTF-MON, the interface that carries the catalog:
// queue depth (gauge, per type), failure rate (counter, per DELIVERY-SIDE
// failure class only — see RecordFailure), unconsumed-expired (counter, per
// type — the "no event misses" signal, INV-DISP-3's
// declared-but-inactive-this-run case), and unknown-type-rejected (counter,
// per type — INV-DISP-3's unknown-to-the-configuration case, which that
// invariant requires be recorded to logs AND metrics). OTel is the default
// emission transport for metrics only (a neutral standard, not a mandated
// backend — GOAL-MIN-1); the concrete sink is a deployment binding via
// INTF-MON.
//
// The Emitter implements eventqueue.Observer, so the queue drives the metrics
// end to end: unconsumed-expired fires from the queue's expiry-sweep path, the
// depth gauge reads the queue's live per-type depth on collect, and failure
// rate fires from the queue's Dispatch path (OnDeclined, fed from a pre-accept
// decline or a dispatch failure — the two delivery-side cases INV-FAIL-1
// covers). It also implements core.IngestObserver, so the core's ingest path
// drives unknown-type-rejected.
//
// Operator scope-cut (2026-07-28): pr-pool measures delivery-side failures
// ONLY. Everything post-accept (retryable / resource-limit / critical, and
// anything that would have been fed from a session-status callback) is
// permanently out of scope — that callback was itself dropped (see
// internal/core's package doc) and pr-pool builds no replacement for it.
//
// This is bead pg2-hvlyj.18 (plan item 5.6), extended by pg2-f3mcb.4 (the
// delivery-side failure wiring above). Its statement coverage is gated at
// >=80% by the `pr-pool-go-tests` flake check (bead pg2-hvlyj.19).
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// Metric names (the declared catalog, INV-OBS-1).
const (
	MetricQueueDepth        = "pr_pool.queue_depth"
	MetricFailures          = "pr_pool.failures"
	MetricUnconsumedExpired = "pr_pool.unconsumed_expired"
	// MetricUnknownTypeRejected counts the ingest-time condition INV-DISP-3
	// requires the core to record to logs AND metrics: an event rejected because no
	// configured binding declares its type. It is the catalog member INTF-MON names
	// for that case — INV-DISP-3's "unknown to the configuration" — so this counter
	// is what makes its "recorded to ... metrics" true. It stays distinct from
	// MetricUnconsumedExpired, which carries the OTHER case (a binding declared but
	// merely inactive this run) plus the ordinary miss.
	MetricUnknownTypeRejected = "pr_pool.unknown_type_rejected"
)

// FailureClassDeclined is the only failure class this Emitter emits today: a
// PRE-ACCEPT decline or an outright dispatch failure — the two delivery-side
// cases INV-FAIL-1 covers, both surfaced through eventqueue.Observer.OnDeclined
// (eventqueue.Queue.Dispatch's Offer()==false branch). The Observer boundary
// does not carry enough information to tell a graceful "busy" decline apart
// from an outright error, so this ONE class is deliberately what is knowable
// there — inventing a finer-grained split the interface cannot support would
// misrepresent the signal, not sharpen it.
const FailureClassDeclined = "declined"

// Emitter emits the core's declared metric catalog over an OTel meter. It
// implements eventqueue.Observer (the queue's own hooks) and core.IngestObserver
// (the ingest-time conditions), so the two paths that produce metrics both drive
// one emitter.
type Emitter struct {
	failures    metric.Int64Counter
	unconsumed  metric.Int64Counter
	unknownType metric.Int64Counter
}

// Ensure the queue can drive it.
var _ eventqueue.Observer = (*Emitter)(nil)

// New registers the instruments above on a meter from mp and returns an Emitter.
// depthFn supplies the current per-type queue depth (typically queue.DepthByType)
// — the observable gauge reads it on each collect, so the gauge tracks
// enqueue/accept/expire without the queue pushing updates.
func New(mp metric.MeterProvider, depthFn func() map[string]int) (*Emitter, error) {
	m := mp.Meter("github.com/phillipgreenii/pr-pool")

	failures, err := m.Int64Counter(
		MetricFailures,
		metric.WithUnit("{failure}"),
		metric.WithDescription("handler-reported failures, per failure class (INV-OBS-1)"),
	)
	if err != nil {
		return nil, err
	}
	unconsumed, err := m.Int64Counter(
		MetricUnconsumedExpired,
		metric.WithUnit("{event}"),
		metric.WithDescription("events that expired with no handler accepting them, per type — under INV-EVT-4 a genuine miss (INV-DISP-3)"),
	)
	if err != nil {
		return nil, err
	}
	unknownType, err := m.Int64Counter(
		MetricUnknownTypeRejected,
		metric.WithUnit("{event}"),
		metric.WithDescription("events rejected at ingest because no configured binding declares their type, per type (INV-DISP-3)"),
	)
	if err != nil {
		return nil, err
	}
	if _, err := m.Int64ObservableGauge(
		MetricQueueDepth,
		metric.WithUnit("{event}"),
		metric.WithDescription("retained non-expired events in the queue, per type (INV-OBS-1)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for typ, depth := range depthFn() {
				o.Observe(int64(depth), metric.WithAttributes(attribute.String("type", typ)))
			}
			return nil
		}),
	); err != nil {
		return nil, err
	}

	return &Emitter{failures: failures, unconsumed: unconsumed, unknownType: unknownType}, nil
}

// OnEnqueue / OnAccept are no-ops: queue depth is observed via the gauge
// callback (reading the live depth), not pushed per event.
func (e *Emitter) OnEnqueue(eventqueue.Event) {}
func (e *Emitter) OnAccept(_, _ string)       {}

// OnUnconsumedExpired increments the unconsumed-expired counter for the event's
// type — the concrete "no event misses" signal (INV-DISP-3), fired from the
// queue's expiry-sweep path.
func (e *Emitter) OnUnconsumedExpired(evtType string) {
	e.unconsumed.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// OnDeclined feeds the failure-rate counter from the queue's Dispatch path
// (eventqueue.Observer): a pre-accept decline or a dispatch failure, the
// delivery-side cases INV-FAIL-1 covers. evtType is accepted for interface
// symmetry with the queue's other per-type hooks but is not itself part of the
// failure-rate label set — the counter's "class" dimension is
// FailureClassDeclined, the one class knowable at this call site (see its doc).
func (e *Emitter) OnDeclined(_ string) {
	e.RecordFailure(FailureClassDeclined)
}

// OnUnknownTypeRejected increments the unknown-type counter for the rejected
// event's type — the metric half of INV-DISP-3's "the condition is recorded to
// logs and metrics", fired from the core's ingest path (it satisfies
// core.IngestObserver).
//
// It counts ONLY the first case, an event type no configured binding declares. An
// event whose binding is merely inactive this run is accepted and expires
// unconsumed, so it lands on OnUnconsumedExpired instead — the two conditions stay
// on separate counters because one is an error and the other is expected.
func (e *Emitter) OnUnknownTypeRejected(evtType string) {
	e.unknownType.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// RecordFailure increments the failure-rate counter for a DELIVERY-SIDE
// failure class (INV-FAIL-1) — a pre-accept decline or a dispatch failure
// where pr-pool could not hand the event over at all. It is KEPT as the
// production entry point (OnDeclined calls it with FailureClassDeclined) so
// the instrument itself, and any future delivery-side class, has one place to
// land; it is exported for direct use in tests.
//
// It is scoped DOWN, not repurposed: retryable / resource-limit / critical —
// everything POST-accept — is permanently out of pr-pool's measurement scope
// (operator scope-cut 2026-07-28) and MUST NOT be recorded here. There is no
// session-status callback to feed those classes from; internal/core dropped it
// outright (see that package's doc) because nothing in pr-pool consumed a
// post-accept outcome.
func (e *Emitter) RecordFailure(class string) {
	e.failures.Add(context.Background(), 1, metric.WithAttributes(attribute.String("class", class)))
}

// Flush forces every metric reader registered on mp to collect and export
// immediately, rather than waiting for its own periodic tick. This is what
// lets a short run — one that starts and finishes between two periodic
// collections — still report what it emitted (INV-OBS-1): callers that are
// about to exit (e.g. `run-until-idle`) call this right before returning.
//
// mp that does not support flushing (the no-op default,
// go.opentelemetry.io/otel/metric/noop.NewMeterProvider) is left untouched —
// that MUST be a safe no-op, never an error, since a MeterProvider is free to
// not implement ForceFlush at all.
func Flush(ctx context.Context, mp metric.MeterProvider) error {
	if f, ok := mp.(interface {
		ForceFlush(context.Context) error
	}); ok {
		return f.ForceFlush(ctx)
	}
	return nil
}
