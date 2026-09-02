// Package metrics emits these members of the core's declared metric catalog
// (INV-OBS-1), each named by INTF-MON, the interface that carries the catalog:
// queue depth (gauge, per type), failure rate (counter, per DELIVERY-SIDE
// failure class — see RecordFailure), unconsumed-expired (counter, per
// type — the "no event misses" signal, INV-DISP-3's
// declared-but-inactive-this-run case), unknown-type-rejected (counter,
// per type — INV-DISP-3's unknown-to-the-configuration case, which that
// invariant requires be recorded to logs AND metrics), throughput (counter,
// per type — events dispatched and accepted), backlog (gauge — a scalar
// sum across all types, distinct from the per-type queue-depth gauge),
// liveness (gauge, daemon-mode only — see WithLiveness), dispatch-latency
// (the catalog's one histogram), source-failures (counter, per source —
// INV-FAIL-3's pull-source failure backoff, the metrics half of a log-only
// path), and deduped (counter, per type — INV-EVT-3's duplicate-id
// visibility). Ten members total (Task 3.3, register gaps R6/pg2-zqpxj,
// R21/pg2-00jpn, and pg2-cz31d). OTel is the default emission transport for
// metrics only (a neutral standard, not a mandated backend — GOAL-MIN-1);
// the concrete sink is a deployment binding via INTF-MON.
//
// The Emitter implements eventqueue.Observer, so the queue drives the metrics
// end to end: unconsumed-expired fires from the queue's expiry-sweep path, the
// depth and backlog gauges read the queue's live per-type depth on collect, and
// failure rate fires from the queue's Dispatch path — OnDeclined for a
// pre-accept decline, OnDispatchFailure for the queue's OTHER delivery-side
// failure class (a recovered panic from a listener's Offer, bead pg2-icm3u) —
// see RecordFailure/FailureClassDeclined/FailureClassDispatchFail. It also
// implements core.IngestObserver, so the core's ingest path drives
// unknown-type-rejected and deduped, and discover.SourceFailureObserver, so
// discover's pull-source retry path drives source-failures.
//
// Throughput, dispatch-latency, and liveness are BUILT and EXPOSED by this
// task (registered on the catalog, with an exported Record method / option)
// but a live production call site for each is a LATER task's concern — see
// RecordThroughput's doc for why. That is this task's own stated scope: build
// and expose the catalog; consuming or further wiring it is for the sibling
// tasks that read this Emitter's exported counters.
//
// Operator scope-cut (2026-07-28): pr-pool measures delivery-side failures
// ONLY. Everything post-accept (retryable / resource-limit / critical, and
// anything that would have been fed from a session-status callback) is
// permanently out of scope — that callback was itself dropped (see
// internal/core's package doc) and pr-pool builds no replacement for it.
//
// This is bead pg2-hvlyj.18 (plan item 5.6), extended by pg2-f3mcb.4 (the
// delivery-side failure wiring above) and by Task 3.3 (the six new catalog
// members and the second failure class above). Its statement coverage is
// gated at >=80% by the `pr-pool-go-tests` flake check (bead pg2-hvlyj.19).
//
// Reader (Task 3.6-prereq) adds the value-READ-BACK half INTF-MON's pull
// direction (`mon.read`, Task 3.6) needs: NewReadableProvider builds an OTel
// MeterProvider with a ManualReader already wired in, and Reader.Snapshot
// collects the catalog's current values from it. Emitter itself stays
// write-only — see Reader's own doc for why the read side is a sibling type
// rather than a method on Emitter.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// Metric names (the declared catalog, INV-OBS-1; ten members, Task 3.3).
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
	// MetricThroughput counts events dispatched and accepted, per type
	// (STORY-OBS-1's "tell a busy system from a stalled one"). See
	// RecordThroughput's doc for why this task builds and exposes it without
	// also wiring a live production call site.
	MetricThroughput = "pr_pool.throughput"
	// MetricBacklog is a scalar sum(DepthByType()) across every type — distinct
	// from the existing per-type MetricQueueDepth gauge (Task 3.3 binding
	// decision).
	MetricBacklog = "pr_pool.backlog"
	// MetricLiveness reports 1 while the daemon's last tick is within its
	// liveness window, else 0. Registered ONLY when New is given WithLiveness
	// (daemon-mode only — Task 3.3 binding decision: drain-and-exit never
	// registers this observable at all, not merely never observes it true).
	MetricLiveness = "pr_pool.liveness"
	// MetricDispatchLatency is the catalog's one Histogram: the time from an
	// event's enqueue to a settling dispatch outcome, in milliseconds. See
	// RecordDispatchLatency's doc for why this task builds and exposes it
	// without also wiring a live production call site.
	MetricDispatchLatency = "pr_pool.dispatch_latency"
	// MetricSourceFailures counts a pull-source query failure, per source —
	// the metrics half of INV-FAIL-3's "reported to logs and metrics, never a
	// silently idle pass" (register gap R21, bead pg2-00jpn). Fed by
	// OnSourceFailure, which discover.go's runAndEnqueue calls (via the
	// SourceFailureObserver seam) on every retry after a pull-source failure,
	// alongside the log-only Warn that already existed there.
	MetricSourceFailures = "pr_pool.source_failures"
	// MetricDeduped counts a duplicate event id the core absorbed because
	// de-duplication already covers it (INV-EVT-3, bead pg2-cz31d), per type.
	// It is a BRAND-NEW counter (not a promotion of any pre-existing field —
	// no such field exists in this repo). Fed by OnDeduped, which
	// internal/core/ingest.go's handleIngestEvent calls (via the extended
	// core.IngestObserver contract) on its existing res == eventqueue.Deduped
	// branch, alongside the Debug log that already existed there.
	MetricDeduped = "pr_pool.deduped"
)

// FailureClassDeclined is fed from eventqueue.Observer.OnDeclined
// (eventqueue.Queue.Dispatch's Offer()==false branch): a pre-accept decline —
// a graceful "busy" decline or an unavailable self-report — the one
// delivery-side class the Observer boundary can currently tell apart from an
// outright dispatch failure (INV-FAIL-1).
const FailureClassDeclined = "declined"

// FailureClassDispatchFail is the catalog's second delivery-side failure
// class (INV-OBS-1): an outright dispatch failure where pr-pool could not
// hand the event over at all, distinct from a graceful pre-accept decline
// (FailureClassDeclined). Its production call site is
// eventqueue.Observer.OnDispatchFailure, fed from eventqueue.Queue.Dispatch's
// offerSafely: a panic recovered from a Listener's own Offer implementation
// is the concrete "the core could not hand the event over at all" condition
// (bead pg2-icm3u — the original Task 3.3 binding decision named "Dispatch's
// error return", a shape that never existed; this is the resolved design).
const FailureClassDispatchFail = "dispatch-failure"

// Emitter emits the core's declared metric catalog over an OTel meter. It
// implements eventqueue.Observer (the queue's own hooks), core.IngestObserver
// (the ingest-time conditions), and discover.SourceFailureObserver (the
// pull-source retry hook), so every path that produces metrics drives one
// emitter.
type Emitter struct {
	failures        metric.Int64Counter
	unconsumed      metric.Int64Counter
	unknownType     metric.Int64Counter
	throughput      metric.Int64Counter
	sourceFailures  metric.Int64Counter
	deduped         metric.Int64Counter
	dispatchLatency metric.Float64Histogram
}

// Ensure the queue can drive it.
var _ eventqueue.Observer = (*Emitter)(nil)

// Option configures an optional catalog member at construction time
// (functional options, so New's existing two-argument call sites need no
// change).
type Option func(*options)

type options struct {
	isLive func() bool
}

// WithLiveness registers MetricLiveness, an ObservableGauge reporting 1 while
// isLive returns true, else 0. Per the Task 3.3 binding decision,
// MetricLiveness is DAEMON-MODE ONLY: a drain-and-exit caller MUST NOT pass
// this option, so New never registers the instrument there at all — skipping
// registration, not merely never observing true, is the letter of that
// decision.
func WithLiveness(isLive func() bool) Option {
	return func(o *options) { o.isLive = isLive }
}

// New registers the instruments above on a meter from mp and returns an Emitter.
// depthFn supplies the current per-type queue depth (typically queue.DepthByType)
// — the observable gauge reads it on each collect, so the gauge tracks
// enqueue/accept/expire without the queue pushing updates.
func New(mp metric.MeterProvider, depthFn func() map[string]int, opts ...Option) (*Emitter, error) {
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}
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
	throughput, err := m.Int64Counter(
		MetricThroughput,
		metric.WithUnit("{event}"),
		metric.WithDescription("events dispatched and accepted, per type (STORY-OBS-1)"),
	)
	if err != nil {
		return nil, err
	}
	sourceFailures, err := m.Int64Counter(
		MetricSourceFailures,
		metric.WithUnit("{failure}"),
		metric.WithDescription("pull-source query failures reported to logs and metrics, per source (INV-FAIL-3)"),
	)
	if err != nil {
		return nil, err
	}
	deduped, err := m.Int64Counter(
		MetricDeduped,
		metric.WithUnit("{event}"),
		metric.WithDescription("duplicate event ids the core absorbed because de-duplication already covers them, per type (INV-EVT-3)"),
	)
	if err != nil {
		return nil, err
	}
	dispatchLatency, err := m.Float64Histogram(
		MetricDispatchLatency,
		metric.WithUnit("ms"),
		metric.WithDescription("time from an event's enqueue to a settling dispatch outcome, in milliseconds (STORY-OBS-1)"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000),
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
	if _, err := m.Int64ObservableGauge(
		MetricBacklog,
		metric.WithUnit("{event}"),
		metric.WithDescription("retained non-expired events across every type, a scalar distinct from the per-type MetricQueueDepth (STORY-OBS-1)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			total := 0
			for _, depth := range depthFn() {
				total += depth
			}
			o.Observe(int64(total))
			return nil
		}),
	); err != nil {
		return nil, err
	}
	if cfg.isLive != nil {
		if _, err := m.Int64ObservableGauge(
			MetricLiveness,
			metric.WithUnit("{liveness}"),
			metric.WithDescription("1 while the daemon's last tick is within its liveness window, else 0; daemon-mode only — drain-and-exit never registers this observable (STORY-OBS-1)"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				v := int64(0)
				if cfg.isLive() {
					v = 1
				}
				o.Observe(v)
				return nil
			}),
		); err != nil {
			return nil, err
		}
	}

	return &Emitter{
		failures:        failures,
		unconsumed:      unconsumed,
		unknownType:     unknownType,
		throughput:      throughput,
		sourceFailures:  sourceFailures,
		deduped:         deduped,
		dispatchLatency: dispatchLatency,
	}, nil
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
// (eventqueue.Observer): a graceful pre-accept decline, one of the two
// delivery-side cases INV-FAIL-1 covers (the other, OnDispatchFailure below,
// fires from the same Dispatch pass for the OTHER class). evtType is accepted
// for interface symmetry with the queue's other per-type hooks but is not
// itself part of the failure-rate label set — the counter's "class" dimension
// is FailureClassDeclined.
func (e *Emitter) OnDeclined(_ string) {
	e.RecordFailure(FailureClassDeclined)
}

// OnDispatchFailure feeds the SAME failure-rate counter as OnDeclined, from
// the SAME queue Dispatch path (eventqueue.Observer), but labeled with the
// OTHER delivery-side class (INV-OBS-1): an outright dispatch failure where
// the core could not hand the event over at all — currently, a panic
// recovered from a Listener's Offer implementation (see
// eventqueue.Queue's offerSafely). evtType is accepted for the same interface-
// symmetry reason OnDeclined's doc gives and is likewise not part of the
// label set; the class dimension is FailureClassDispatchFail.
func (e *Emitter) OnDispatchFailure(_ string) {
	e.RecordFailure(FailureClassDispatchFail)
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

// OnSourceFailure implements discover.SourceFailureObserver (the interface is
// defined in internal/discover to keep the dependency direction the queue's
// own Observer already uses: the producer side declares the hook, metrics
// implements it). It increments the source-failures counter for a pull
// source whose query failed and is about to retry after backoff
// (INV-FAIL-3, register gap R21 / bead pg2-00jpn) — the metrics half of the
// log-only Warn line discover.go's runAndEnqueue already writes at that same
// retry point.
func (e *Emitter) OnSourceFailure(source string) {
	e.sourceFailures.Add(context.Background(), 1, metric.WithAttributes(attribute.String("source", source)))
}

// OnDeduped implements the extended core.IngestObserver contract. It
// increments the deduped counter, per type, when ingest-event absorbs a
// duplicate id still retained in the queue (INV-EVT-3, bead pg2-cz31d) — the
// metrics half of the Debug log line internal/core/ingest.go's
// handleIngestEvent already writes at that same res == eventqueue.Deduped
// branch.
func (e *Emitter) OnDeduped(evtType string) {
	e.deduped.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// RecordThroughput increments the throughput counter, per type, for an event
// dispatched and accepted (STORY-OBS-1). Exported for direct/test use:
// eventqueue.Observer.OnAccept's signature carries (eventID, listenerID)
// only, not the event's type, so wiring this directly into that hook would
// require changing the Observer interface — outside this task's Files scope.
// This task's own stated scope is to build and expose the catalog; a live
// call site (fed with the type from wherever it is actually known, e.g. once
// a future task threads it through) is left to that later task, the same way
// Task 3.5 is documented to read this Emitter's exported counters.
func (e *Emitter) RecordThroughput(evtType string) {
	e.throughput.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// RecordDispatchLatency records the elapsed time, in milliseconds, from an
// event's enqueue to a settling dispatch outcome (STORY-OBS-1) — the
// catalog's one histogram. Exported for direct/test use; see
// RecordThroughput's doc for why this task stops at building and exposing
// the instrument rather than also wiring a live dispatch-timing call site
// (measuring it would need per-event enqueue-time bookkeeping that is its
// own design decision, not a byproduct of this task's Files).
func (e *Emitter) RecordDispatchLatency(ms float64) {
	e.dispatchLatency.Record(context.Background(), ms)
}

// Reader is a value-read-back handle over the catalog's current counter
// values (INTF-MON pull; Task 3.6-prereq). It wraps an OTel
// sdkmetric.ManualReader — a sibling of Emitter, not a method on it, because
// the read side needs a handle bound at MeterProvider CONSTRUCTION time (an
// OTel reader is fixed into a MeterProvider's option list when the provider
// is built, and Emitter is constructed AFTER the MeterProvider already
// exists, from an mp it merely calls Meter() on). See NewReadableProvider,
// which builds both together.
type Reader struct {
	reader *sdkmetric.ManualReader
}

// NewReadableProvider returns a fresh OTel SDK MeterProvider with a
// ManualReader wired in, plus the Reader handle that collects from it. Any
// instrument created via Meter() on the returned MeterProvider (e.g. by
// passing it to New) can have its current value read back through the
// returned Reader's Snapshot — this is what lets core.Service answer
// mon.read (Task 3.6) without the metrics package depending on
// internal/core, or internal/core depending on the OTel SDK's concrete
// exporter types.
//
// This is a DIFFERENT default from Config.Meter()'s own documented default
// (the no-op provider, INV-OBS-1 / Task 3.3 binding decision: "core stays
// unaware of any concrete monitoring backend"): a no-op provider's
// instruments never record anything, so it can never answer a read-back
// query. cmd/pr-pool's bootCore is expected to call this ONLY when no
// deployment-bound MeterProvider is configured (cfg.MeterProvider unset) —
// see its resolveMeterProvider — never as a second reader retrofitted onto
// an already-constructed external provider, which the OTel SDK does not
// support.
func NewReadableProvider() (metric.MeterProvider, *Reader) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return mp, &Reader{reader: reader}
}

// Snapshot collects the catalog's current values from every instrument
// registered on the MeterProvider NewReadableProvider returned alongside
// this Reader. It returns OTel's own neutral snapshot type
// (metricdata.ResourceMetrics — see the package doc's "a neutral standard,
// not a mandated backend") rather than a pr-pool-specific shape: filtering
// it down to one sink's configured subset and translating it into the
// mon.read-reply wire shape is Task 3.6's job, not this prereq's (Task 3.6
// Binding decisions).
func (r *Reader) Snapshot(ctx context.Context) (metricdata.ResourceMetrics, error) {
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(ctx, &rm); err != nil {
		return metricdata.ResourceMetrics{}, err
	}
	return rm, nil
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
