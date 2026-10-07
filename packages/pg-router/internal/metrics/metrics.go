// Package metrics emits these members of the core's declared metric catalog
// (INV-OBS-1), each named by INTF-MON, the interface that carries the catalog:
// queue depth (gauge, per type), failure rate (counter, per DELIVERY-SIDE
// failure class — see RecordFailure; the "declined" class ALSO carries a
// second, additive "reason" label — see OnDeclined's own doc, bead
// pg2-j4uwg), unconsumed-expired (counter, per
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
// implements orchestrator.HandlerFailureObserver (structurally, without an
// import — see OnHandlerFailure's own doc), a THIRD delivery-side failure
// class fed from roleListener.Offer's own non-panic handler error return
// (bead pg2-97539 — FailureClassHandlerError), a case the queue itself never
// sees because that bridge "always reports acceptance" (ADR 0056). It also
// implements core.IngestObserver, so the core's ingest path drives
// unknown-type-rejected and deduped, and discover.SourceFailureObserver, so
// discover's pull-source retry path drives source-failures.
//
// Throughput and dispatch-latency are fed from OnAccept, via the eventID
// correlation map OnEnqueue populates — see RecordThroughput's doc for why
// (OnAccept's signature carries no event type or timing) and
// RecordDispatchLatency's doc for the outcome label's current scope
// (accepted only; other outcomes are a deferred follow-up). Liveness is fed
// from the isLive callback passed to WithLiveness, evaluated live on each
// collect.
//
// Operator scope-cut (2026-07-28): pg-router measures delivery-side failures
// ONLY. Everything post-accept (retryable / resource-limit / critical, and
// anything that would have been fed from a session-status callback) is
// permanently out of scope — that callback was itself dropped (see
// internal/core's package doc) and pg-router builds no replacement for it.
//
// This is bead pg2-hvlyj.18 (plan item 5.6), extended by pg2-f3mcb.4 (the
// delivery-side failure wiring above) and by Task 3.3 (the six new catalog
// members and the second failure class above). Its statement coverage is
// gated at >=80% by the `pg-router-go-tests` flake check (bead pg2-hvlyj.19).
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
	"errors"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// Metric names (the declared catalog, INV-OBS-1; ten members, Task 3.3).
//
// Registered with underscore/legacy-safe names directly (pg2-y3u22, operator
// decision 2026-09-18), NOT OTel's usual dotted convention: Prometheus 3.x's
// UTF-8 metric-name-escaping negotiation is honored by the OTel Prometheus
// bridge (cmd/pg-router/metrics_http.go), so a dotted name like
// "pg_router.backlog" was exposed to Prometheus verbatim with the dot
// preserved instead of being translated to "pg_router_backlog" — the
// pg-router.json Grafana dashboard queries the underscore names (matching
// the established convention already used by pg-pr-ops.json), so those
// panels rendered empty. Naming the instrument with underscores up front
// means Prometheus never needs to escape-negotiate a dotted name in the
// first place, regardless of which scheme the scraper offers.
const (
	MetricQueueDepth        = "pg_router_queue_depth"
	MetricFailures          = "pg_router_failures"
	MetricUnconsumedExpired = "pg_router_unconsumed_expired"
	// MetricUnknownTypeRejected counts the ingest-time condition INV-DISP-3
	// requires the core to record to logs AND metrics: an event rejected because no
	// configured binding declares its type. It is the catalog member INTF-MON names
	// for that case — INV-DISP-3's "unknown to the configuration" — so this counter
	// is what makes its "recorded to ... metrics" true. It stays distinct from
	// MetricUnconsumedExpired, which carries the OTHER case (a binding declared but
	// merely inactive this run) plus the ordinary miss.
	MetricUnknownTypeRejected = "pg_router_unknown_type_rejected"
	// MetricThroughput counts events dispatched and accepted, per type
	// (STORY-OBS-1's "tell a busy system from a stalled one"). See
	// RecordThroughput's doc for why this task builds and exposes it without
	// also wiring a live production call site.
	MetricThroughput = "pg_router_throughput"
	// MetricBacklog is a scalar sum(DepthByType()) across every type — distinct
	// from the existing per-type MetricQueueDepth gauge (Task 3.3 binding
	// decision).
	MetricBacklog = "pg_router_backlog"
	// MetricQueueLogBytes is the current byte size of the durable event-queue
	// write-ahead log (<LogDir>/queue.jsonl) — events AND gates (bead pg2-8e0m6).
	// Registered only when New is given WithQueueLogSize. It rises with every
	// write and drops when the log is compacted, so a flat-lining saw-tooth is
	// healthy and a line that only climbs means compaction is not keeping up.
	MetricQueueLogBytes = "pg_router_queue_log_bytes"
	// The log-size limit's metrics (bead pg2-5d3ui; internal/eventqueue/limits.go).
	// Registered only when New is given WithLogLimitStatus.
	//
	// MetricQueueLogLimitBytes is the hard limit (max_log_bytes); MetricQueueLogPercent
	// is MetricQueueLogBytes as a percentage (0-100, may exceed 100) of it.
	MetricQueueLogLimitBytes = "pg_router_queue_log_limit_bytes"
	MetricQueueLogPercent    = "pg_router_queue_log_percent"
	// MetricEmittersHalted is 1 while polled command-source emitters are not being
	// polled (past the soft threshold after a compaction attempt, or the log is
	// unwritable), else 0. It is the soft step's whole metric surface: the soft
	// step is not a gate, so MetricActiveGates / MetricGateBlocked do not move.
	MetricEmittersHalted = "pg_router_emitters_halted"
	// MetricLogRejecting is 1 per reason ("log_full", "log_unwritable") while the
	// log is in that state and refusing new events, else 0.
	MetricLogRejecting = "pg_router_log_rejecting"
	// MetricEnqueueRejected counts event admissions refused at Enqueue, per event
	// type and reason ("log_full" / "log_unwritable"); push and pull together
	// (eventqueue.RejectObserver).
	MetricEnqueueRejected = "pg_router_enqueue_rejected"
	// MetricDispatchRetries counts each re-run the core schedules for a
	// dispatch whose handler failed with a transient error (bead pg2-yu5y2), per
	// role and class (killed / deadline / unavailable). It is the only
	// post-accept-adjacent signal besides the handler-error class and exists so
	// a retry storm is visible; the cap on retries per event is
	// roles.MaxDispatchRetriesCap.
	MetricDispatchRetries = "pg_router_dispatch_retries"
	// MetricLiveness reports 1 while the daemon's last tick is within its
	// liveness window, else 0. Registered ONLY when New is given WithLiveness
	// (daemon-mode only — Task 3.3 binding decision: drain-and-exit never
	// registers this observable at all, not merely never observes it true).
	MetricLiveness = "pg_router_liveness"
	// MetricDispatchLatency is the catalog's original Histogram: the time from an
	// event's enqueue to a settling dispatch outcome, in milliseconds. See
	// RecordDispatchLatency's doc for why this task builds and exposes it
	// without also wiring a live production call site.
	//
	// DEPRECATED (bead pg2-n7da9): it is wait + run with no way to tell them
	// apart, so it cannot answer "is this listener slow, or is its queue long".
	// MetricQueueWait and MetricRun split it. It is KEPT, unchanged, because the
	// pg-router dashboard's p50/p95 panels still query it; remove it once those
	// panels move to the new pair.
	MetricDispatchLatency = "pg_router_dispatch_latency"
	// MetricQueueWait and MetricRun are the split of MetricDispatchLatency (bead
	// pg2-n7da9), in seconds, per role and FULL event type (pr.changed and
	// pr.reconcile are distinct series): MetricQueueWait is the time from the
	// event's enqueue (Event.At) to the instant the accepting listener's offer went
	// in-flight; MetricRun is that offer's own synchronous handler run. Both are fed
	// from eventqueue.TimingObserver, once per accepted dispatch (a handler that
	// returned an error still counts: the queue sees it as accepted, see
	// FailureClassHandlerError). Exposed to Prometheus as pg_router_queue_wait_seconds
	// and pg_router_run_seconds.
	MetricQueueWait = "pg_router_queue_wait"
	MetricRun       = "pg_router_run"
	// MetricQueueOldestAge is a gauge, per event type: the age in seconds of the
	// oldest retained event of that type that a bound listener has not yet settled
	// (eventqueue.Queue.OldestPendingAgeByType). The histograms learn of a wait only
	// when it ends; this shows a stuck head while it is still growing. A type with
	// nothing owed reads 0. Exposed as pg_router_queue_oldest_age_seconds.
	MetricQueueOldestAge = "pg_router_queue_oldest_age"
	// MetricListenerInFlight is a gauge, per role: 1 while the listener has an offer
	// outstanding, else 0 (eventqueue.Queue.InFlightByListener). INV-CONC-1 caps it
	// at 1; the time-average of it is the lane's utilisation. Exposed as
	// pg_router_listener_in_flight.
	MetricListenerInFlight = "pg_router_listener_in_flight"
	// MetricSourceFailures counts a pull-source query failure, per source and
	// reason (see ClassifySourceFailure; the alert sums the reason away) —
	// the metrics half of INV-FAIL-3's "reported to logs and metrics, never a
	// silently idle pass" (register gap R21, bead pg2-00jpn). Fed by
	// OnSourceFailure, which discover.go's runAndEnqueue calls (via the
	// SourceFailureObserver seam) on every failed pull-source query attempt:
	// each one that will be retried after backoff, and the final give-up one
	// (so a fail-fast source with no retries is counted too, bead pg2-jgbnp).
	MetricSourceFailures = "pg_router_source_failures"
	// MetricDeduped counts a duplicate event id the core absorbed because
	// de-duplication already covers it (INV-EVT-3, bead pg2-cz31d), per type.
	// It is a BRAND-NEW counter (not a promotion of any pre-existing field —
	// no such field exists in this repo). Fed by OnDeduped, which
	// internal/core/ingest.go's handleIngestEvent calls (via the extended
	// core.IngestObserver contract) on its existing res == eventqueue.Deduped
	// branch, alongside the Debug log that already existed there.
	MetricDeduped = "pg_router_deduped"
)

// The per-source liveness gauges (bead pg2-tv11a, DEC-OBS-4). They are NOT part
// of the ten-member INV-OBS-1 catalog's count; they are the two gauges
// INTF-MON lists after it, registered only when New is given WithPullSources.
// Both are Float64 gauges in unit "s", so the Prometheus exposition appends
// "_seconds": pg_router_source_last_success_timestamp_seconds{source} and
// pg_router_source_expected_interval_seconds{source}.
const (
	// MetricSourceLastSuccess is the Unix time (seconds) of the source's last
	// successful pass — or of the last deliberate pause (gate / halted
	// emitters), which is not a failure — initialised to process start. A
	// failed pass never advances it.
	MetricSourceLastSuccess = "pg_router_source_last_success_timestamp"
	// MetricSourceExpectedInterval is the source's configured period in
	// seconds, the unit the persistent-failure alert scales its threshold by.
	MetricSourceExpectedInterval = "pg_router_source_expected_interval"
)

// The per-source attempt-timing metrics (bead pg2-zdowv, DEC-OBS-8). Like the
// liveness gauges above they sit outside the INV-OBS-1 catalog's count.
const (
	// MetricSourceDuration is a histogram, in seconds (Prometheus
	// pg_router_source_duration_seconds), of how long one pull-source query
	// ATTEMPT ran, per source. It is recorded for every attempt that ran to its
	// own end, success or failure — a source killed by the 30s scriptout timeout
	// lands in the 30s bucket — and NOT for an attempt cut short by the router's
	// own shutdown.
	MetricSourceDuration = "pg_router_source_duration"
	// MetricSourceInflightChildren is a gauge, per source, of query attempts
	// (the source's child process) currently running. A source whose value
	// stays at 1 for longer than its timeout is a hung child.
	MetricSourceInflightChildren = "pg_router_source_inflight_children"
)

// The Gate Registry's metrics (bead pg2-h63eu; internal/eventqueue/gate.go).
// They are NOT part of the ten-member INV-OBS-1 catalog above: they observe a
// separate mechanism and are registered alongside it by the same Emitter, which
// also implements eventqueue.GateObserver.
//
// CARDINALITY. A gate TYPE is arbitrary, so every "type"/"gate" label below goes
// through Emitter.gateLabel, which accepts up to maxGateTypeLabels distinct
// TYPEs and folds any further ones into GateLabelOther. A gate's OWNER is
// DEBUG ONLY and is deliberately NEVER a metric label (unbounded cardinality):
// it is logged and shown in the TUI instead.
const (
	// MetricActiveGates reports 1 per gate TYPE currently in force (gauge).
	MetricActiveGates = "pg_router_gates_active"
	// MetricGateSets counts GateSet records per TYPE; renewal="true" marks one
	// that replaced an already-active gate (a lease renewal or overwrite).
	MetricGateSets = "pg_router_gate_sets"
	// MetricGateClears counts gates removed by a clear, per TYPE.
	MetricGateClears = "pg_router_gate_clears"
	// MetricGateExpiries counts gates retired by TTL expiry, per TYPE.
	MetricGateExpiries = "pg_router_gate_expiries"
	// MetricGateDuration is how long a gate was in force (seconds), per TYPE and
	// outcome ("cleared" or "expired").
	MetricGateDuration = "pg_router_gate_duration"
	// MetricGateBlocked counts the times a gate stopped a participant from doing
	// work it had: per participant, kind ("dispatch", "poll" or "pull") and gate
	// TYPE.
	MetricGateBlocked = "pg_router_gate_blocked"
	// MetricGateDrops counts events that went away for a gate-blocked listener at
	// its final attempt (INV-EVT-4), per event type, role (the listener id, DEC-OBS-7)
	// and gate TYPE — the measure of how much routing a long gate cost.
	MetricGateDrops = "pg_router_gate_drops"
)

// maxGateTypeLabels bounds how many distinct gate TYPE label values the
// Emitter will mint; GateLabelOther collects the rest.
const maxGateTypeLabels = 64

// GateLabelOther is the label value gate TYPEs beyond maxGateTypeLabels share.
const GateLabelOther = "OTHER"

// FailureClassDeclined is fed from eventqueue.Observer.OnDeclined
// (eventqueue.Queue.Dispatch's Offer()==false branch): a pre-accept decline —
// a graceful "busy" decline or an unavailable self-report — the one
// delivery-side class the Observer boundary can currently tell apart from an
// outright dispatch failure (INV-FAIL-1).
const FailureClassDeclined = "declined"

// FailureClassDispatchFail is the catalog's second delivery-side failure
// class (INV-OBS-1): an outright dispatch failure where pg-router could not
// hand the event over at all, distinct from a graceful pre-accept decline
// (FailureClassDeclined). Its production call site is
// eventqueue.Observer.OnDispatchFailure, fed from eventqueue.Queue.Dispatch's
// offerSafely: a panic recovered from a Listener's own Offer implementation
// is the concrete "the core could not hand the event over at all" condition
// (bead pg2-icm3u — the original Task 3.3 binding decision named "Dispatch's
// error return", a shape that never existed; this is the resolved design).
const FailureClassDispatchFail = "dispatch-failure"

// FailureClassHandlerError is the catalog's THIRD delivery-side failure
// class (bead pg2-97539): a genuine, non-panic error the registered handler
// participant itself reported through wireclient's synchronous Offer call
// (internal/orchestrator/listener.go's roleListener.Offer) — e.g. `wireclient:
// role "review" exited 1: <bead>: session exited before completing`. This is
// a DIFFERENT failure shape from FailureClassDispatchFail: that class is the
// core crashing while offering (a recovered panic, offerSafely never even
// reaching the handler's own reported outcome); this class is the handler
// running to completion and reporting failure as an ordinary error return.
// Per ADR 0056's "always reports acceptance," roleListener.Offer still
// returns an ACCEPTED OfferResult for this case (eventqueue.Observer never
// sees a decline or a panic for it), so before this class existed the error
// was logged (emitResult) and otherwise dropped — pg_router_failures_total
// never incremented. Its production call site is
// orchestrator.HandlerFailureObserver.OnHandlerFailure, fed from Offer
// whenever workOne's error is non-nil and NOT wireclient.ErrBusy (that case
// stays on FailureClassDeclined, via the pre-accept eventqueue.Observer
// path, to avoid double-counting).
const FailureClassHandlerError = "handler-error"

// BudgetExceededSentinel is the documented, stable substring a handler's error
// text carries when its session hit its budget (docs/behavior/interfaces.md,
// "Budget-stop sentinel"). The core cannot errors.Is across the process
// boundary, so it matches on this text. (pg2-irowq)
const BudgetExceededSentinel = "session budget exceeded"

// ReasonBudgetExceeded is the "reason" label a FailureClassHandlerError series
// carries when the handler error contains BudgetExceededSentinel. (pg2-irowq)
const ReasonBudgetExceeded = "budget-exceeded"

// ReasonTriagerFailure is the "reason" label a FailureClassHandlerError series
// carries when the failing role is an escalation-triage role (IsTriagerRole).
// It bulkheads triager-dispatch failures (including a triager that cannot
// observe an item's completion) out of the worker/review failure-rate series
// so they cannot fire pg-router-failure-rate; they are alerted on separately
// (pg-router-triager-failures). It takes precedence over ReasonBudgetExceeded:
// a triager hitting its budget is still a triager failure. (pg2-u2yub)
const ReasonTriagerFailure = "triager-failure"

// TriagerRoleMarker is the substring that marks a role name as an
// escalation-triage role. It deliberately mirrors the handler's own
// convention (pg-router-ccpool-handler's humanOnlyReason treats a role as
// "triage" when its name contains "triage"), so one naming rule serves both
// sides without a new role-config field. (pg2-u2yub)
const TriagerRoleMarker = "triage"

// IsTriagerRole reports whether role names an escalation-triage role.
func IsTriagerRole(role string) bool {
	return strings.Contains(role, TriagerRoleMarker)
}

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
	enqueueRejected metric.Int64Counter
	dispatchRetries metric.Int64Counter
	dispatchLatency metric.Float64Histogram
	queueWait       metric.Float64Histogram
	run             metric.Float64Histogram

	// typeMu guards typeSeen, the bounded set of full event-type label values the
	// wait/run histograms have minted (see eventTypeLabel).
	typeMu   sync.Mutex
	typeSeen map[string]struct{}

	// Gate Registry instruments (gate.go's GateObserver half; see the const
	// block above).
	gateSets     metric.Int64Counter
	gateClears   metric.Int64Counter
	gateExpiries metric.Int64Counter
	gateDuration metric.Float64Histogram
	gateBlocked  metric.Int64Counter
	gateDrops    metric.Int64Counter
	gateMu       sync.Mutex
	gateSeen     map[string]struct{}

	now func() time.Time

	// srcMu guards srcLastSuccess. The key set is fixed at construction
	// (WithPullSources); OnSourceSucceeded / OnSourcePaused for any other
	// name are ignored, which bounds the gauge's cardinality.
	srcMu          sync.Mutex
	srcLastSuccess map[string]time.Time
	// srcInflight is the per-source count of running query attempts (also
	// guarded by srcMu). Keys are config-bounded source names; every
	// WithPullSources source is seeded at 0 so its series exists from start.
	srcInflight map[string]int
	srcDuration metric.Float64Histogram

	// mu guards pending/order — the eventID->{type, enqueue-time} correlation
	// map OnAccept needs to feed RecordThroughput/RecordDispatchLatency (see
	// their own docs for why: OnAccept's signature carries no event type or
	// timing). Mirrors cmd/pg-router/run.go's activityObserver.pending/order
	// exactly, as its own doc predicted this package would.
	mu      sync.Mutex
	pending map[string]pendingDispatch
	order   []string // insertion order, for FIFO eviction
}

// dispatchPendingCap bounds Emitter's eventID->pendingDispatch correlation
// map — same shape and value as run.go's activityPendingTypesCap, which
// solves the identical OnAccept-carries-no-type gap for the activity ring.
const dispatchPendingCap = 4096

// pendingDispatch is what OnEnqueue records for one still-outstanding event,
// for OnAccept to consult: the type (RecordThroughput's label) and the
// resolved enqueue instant (evt.At) RecordDispatchLatency measures elapsed
// time from.
type pendingDispatch struct {
	typ        string
	enqueuedAt time.Time
}

// Ensure the queue can drive it.
var (
	_ eventqueue.Observer        = (*Emitter)(nil)
	_ eventqueue.GateObserver    = (*Emitter)(nil)
	_ eventqueue.RestoreObserver = (*Emitter)(nil)
	_ eventqueue.RejectObserver  = (*Emitter)(nil)
	_ eventqueue.TimingObserver  = (*Emitter)(nil)
)

// Option configures an optional catalog member at construction time
// (functional options, so New's existing two-argument call sites need no
// change).
type Option func(*options)

type options struct {
	queueLogBytes func() int64
	logLimits     func() eventqueue.LimitStatus
	activeGates   func() []eventqueue.Gate
	isLive        func() bool
	now           func() time.Time
	poolDir       string
	poolTTL       time.Duration
	pullSources   map[string]time.Duration
	queueAges     func() map[string]time.Duration
	listenerBusy  func() map[string]int
}

// WithClock injects a clock seam (default time.Now) for deterministic tests
// of RecordDispatchLatency's production call site (OnAccept), which measures
// elapsed time since an event's OnEnqueue — mirrors eventqueue.WithClock.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// WithQueueAges registers MetricQueueOldestAge, an ObservableGauge reading fn
// (typically queue.OldestPendingAgeByType) on each collect. It exports one series
// per type in the queue's depth map, 0 for a type with nothing owed, plus any
// type fn itself reports. Without the option the gauge is not registered.
func WithQueueAges(fn func() map[string]time.Duration) Option {
	return func(o *options) { o.queueAges = fn }
}

// WithListenerInFlight registers MetricListenerInFlight, an ObservableGauge
// reading fn (typically queue.InFlightByListener) on each collect: one series per
// registered listener, 1 while it has an offer outstanding else 0. Without the
// option the gauge is not registered.
func WithListenerInFlight(fn func() map[string]int) Option {
	return func(o *options) { o.listenerBusy = fn }
}

// WithPullSources registers the per-source liveness gauges
// (MetricSourceLastSuccess and MetricSourceExpectedInterval) for exactly the
// named sources, each mapped to its expected interval. The caller passes only
// enabled, non-excluded PULL sources that have a period (bead pg2-tv11a);
// an entry with a non-positive interval is skipped. Every registered source's
// last-success time starts at the Emitter's clock at construction (process
// start), NOT zero, so a restart resets the clock. Without this option the
// gauges are not registered.
func WithPullSources(sources map[string]time.Duration) Option {
	return func(o *options) { o.pullSources = sources }
}

// WithQueueLogSize registers MetricQueueLogBytes, an ObservableGauge reading fn
// (typically queue.LogSize) on each collect. Without it the gauge is simply not
// registered.
func WithQueueLogSize(fn func() int64) Option {
	return func(o *options) { o.queueLogBytes = fn }
}

// WithLogLimitStatus registers the log-size limit's gauges — MetricQueueLogLimitBytes,
// MetricQueueLogPercent, MetricEmittersHalted and MetricLogRejecting — reading fn
// (typically queue.LimitStatus) on each collect. Without it they are simply not
// registered. MetricEnqueueRejected (a counter, fed by OnEnqueueRejected) is
// always registered.
func WithLogLimitStatus(fn func() eventqueue.LimitStatus) Option {
	return func(o *options) { o.logLimits = fn }
}

// WithActiveGates registers MetricActiveGates, an ObservableGauge reporting 1
// per gate TYPE returned by fn (typically queue.ActiveGates). Without it the
// gauge is simply not registered.
func WithActiveGates(fn func() []eventqueue.Gate) Option {
	return func(o *options) { o.activeGates = fn }
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
	cfg := options{now: time.Now}
	for _, opt := range opts {
		opt(&cfg)
	}
	m := mp.Meter("github.com/phillipgreenii/pg-router")

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
		metric.WithDescription("pull-source query failures reported to logs and metrics, per source and reason (INV-FAIL-3)"),
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
	enqueueRejected, err := m.Int64Counter(
		MetricEnqueueRejected,
		metric.WithUnit("{event}"),
		metric.WithDescription("event admissions refused because the event log is at its hard size limit (reason=log_full) or cannot be written (reason=log_unwritable), per event type; push and pull together"),
	)
	if err != nil {
		return nil, err
	}
	dispatchRetries, err := m.Int64Counter(
		MetricDispatchRetries,
		metric.WithUnit("{retry}"),
		metric.WithDescription("re-runs the core scheduled for a dispatch whose handler failed with a transient error, per role and class (killed/deadline/unavailable); bounded per event by the role's max_dispatch_retries"),
	)
	if err != nil {
		return nil, err
	}
	dispatchLatency, err := m.Float64Histogram(
		MetricDispatchLatency,
		metric.WithUnit("ms"),
		metric.WithDescription("DEPRECATED (use pg_router_queue_wait_seconds and pg_router_run_seconds): time from an event's enqueue to a settling dispatch outcome, in milliseconds, wait plus run with no split (STORY-OBS-1)"),
		// Seconds-to-hours scale: latency is measured from evt.At through the
		// synchronous handler run (bead pg2-nimab), and a re-offered event can be
		// older than an hour (+Inf covers it).
		metric.WithExplicitBucketBoundaries(100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000, 600000, 1200000, 1800000, 3600000),
	)
	if err != nil {
		return nil, err
	}
	queueWait, err := m.Float64Histogram(
		MetricQueueWait,
		metric.WithUnit("s"),
		metric.WithDescription("time an accepted event waited, in seconds, from its enqueue to the moment the accepting role's offer went in-flight, per role and full event type (pg2-n7da9)"),
		metric.WithExplicitBucketBoundaries(timingBuckets...),
	)
	if err != nil {
		return nil, err
	}
	run, err := m.Float64Histogram(
		MetricRun,
		metric.WithUnit("s"),
		metric.WithDescription("synchronous handler run time of an accepted dispatch, in seconds, from the role's offer going in-flight to its return, per role and full event type; a handler that returned an error is included (pg2-n7da9)"),
		metric.WithExplicitBucketBoundaries(timingBuckets...),
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
	if cfg.queueAges != nil {
		if _, err := m.Float64ObservableGauge(
			MetricQueueOldestAge,
			metric.WithUnit("s"),
			metric.WithDescription("age in seconds of the oldest retained event, per type, that a bound listener has not yet settled; 0 when nothing of that type is owed (pg2-n7da9)"),
			metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
				ages := cfg.queueAges()
				seen := map[string]bool{}
				for typ, age := range ages {
					seen[typ] = true
					o.Observe(age.Seconds(), metric.WithAttributes(attribute.String("type", typ)))
				}
				for typ := range depthFn() {
					if !seen[typ] {
						o.Observe(0, metric.WithAttributes(attribute.String("type", typ)))
					}
				}
				return nil
			}),
		); err != nil {
			return nil, err
		}
	}
	if cfg.listenerBusy != nil {
		if _, err := m.Int64ObservableGauge(
			MetricListenerInFlight,
			metric.WithUnit("{dispatch}"),
			metric.WithDescription("1 while a role's listener has an offer outstanding, else 0, per role; INV-CONC-1 caps it at 1 (pg2-n7da9)"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				for role, n := range cfg.listenerBusy() {
					o.Observe(int64(n), metric.WithAttributes(attribute.String("role", role)))
				}
				return nil
			}),
		); err != nil {
			return nil, err
		}
	}
	e := &Emitter{gateSeen: map[string]struct{}{}, srcInflight: map[string]int{}, typeSeen: map[string]struct{}{}}
	if err := e.registerSourceTiming(m, cfg.pullSources); err != nil {
		return nil, err
	}
	if len(cfg.pullSources) > 0 {
		if err := e.registerSourceGauges(m, cfg.pullSources, cfg.now()); err != nil {
			return nil, err
		}
	}
	if cfg.queueLogBytes != nil {
		if _, err := m.Int64ObservableGauge(
			MetricQueueLogBytes,
			metric.WithUnit("By"),
			metric.WithDescription("size of the durable event-queue write-ahead log (queue.jsonl); drops when the log is compacted"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				o.Observe(cfg.queueLogBytes())
				return nil
			}),
		); err != nil {
			return nil, err
		}
	}
	if cfg.logLimits != nil {
		if err := registerLogLimitGauges(m, cfg.logLimits); err != nil {
			return nil, err
		}
	}
	if cfg.activeGates != nil {
		if _, err := m.Int64ObservableGauge(
			MetricActiveGates,
			metric.WithUnit("{gate}"),
			metric.WithDescription("1 per gate TYPE currently in force (Gate Registry); an empty series means nothing is gated"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				for _, g := range cfg.activeGates() {
					o.Observe(1, metric.WithAttributes(attribute.String("type", e.gateLabel(g.Type))))
				}
				return nil
			}),
		); err != nil {
			return nil, err
		}
	}
	gateSets, err := m.Int64Counter(MetricGateSets, metric.WithUnit("{record}"),
		metric.WithDescription("GateSet records written, per gate TYPE (renewal=true when it replaced an active gate)"))
	if err != nil {
		return nil, err
	}
	gateClears, err := m.Int64Counter(MetricGateClears, metric.WithUnit("{gate}"),
		metric.WithDescription("gates removed by a clear, per gate TYPE"))
	if err != nil {
		return nil, err
	}
	gateExpiries, err := m.Int64Counter(MetricGateExpiries, metric.WithUnit("{gate}"),
		metric.WithDescription("gates retired by TTL expiry, per gate TYPE"))
	if err != nil {
		return nil, err
	}
	gateDuration, err := m.Float64Histogram(MetricGateDuration, metric.WithUnit("s"),
		metric.WithDescription("how long a gate was in force, per gate TYPE and outcome (cleared|expired)"),
		metric.WithExplicitBucketBoundaries(1, 10, 30, 60, 300, 900, 3600, 14400, 86400))
	if err != nil {
		return nil, err
	}
	gateBlocked, err := m.Int64Counter(MetricGateBlocked, metric.WithUnit("{block}"),
		metric.WithDescription("times a gate stopped a participant from working, per participant, kind (dispatch|poll|pull) and gate TYPE"))
	if err != nil {
		return nil, err
	}
	gateDrops, err := m.Int64Counter(MetricGateDrops, metric.WithUnit("{event}"),
		metric.WithDescription("events that went away for a gate-blocked listener at its final attempt, per event type, listener and gate TYPE"))
	if err != nil {
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

	if cfg.poolDir != "" {
		if err := registerWorktreePool(m, newPoolScanner(cfg.poolDir, cfg.poolTTL, cfg.now)); err != nil {
			return nil, err
		}
	}

	e.failures = failures
	e.unconsumed = unconsumed
	e.unknownType = unknownType
	e.throughput = throughput
	e.sourceFailures = sourceFailures
	e.deduped = deduped
	e.enqueueRejected = enqueueRejected
	e.dispatchRetries = dispatchRetries
	e.dispatchLatency = dispatchLatency
	e.queueWait = queueWait
	e.run = run
	e.gateSets = gateSets
	e.gateClears = gateClears
	e.gateExpiries = gateExpiries
	e.gateDuration = gateDuration
	e.gateBlocked = gateBlocked
	e.gateDrops = gateDrops
	e.now = cfg.now
	e.pending = map[string]pendingDispatch{}
	e.preRecordZeroCounters()
	return e, nil
}

// preRecordZeroCounters adds 0 to the counters that are otherwise silent until
// their (rare) first event, so each exports a label-less zero series from
// startup. A zero-increment OTel counter that has never been Add()ed exports no
// series, leaving Prometheus without a `_total` to rate()/absent() against
// (pg2-9q3pq). The label-less series sits beside, and is never merged with, the
// labelled per-type series the real events create, so sum() is unaffected.
func (e *Emitter) preRecordZeroCounters() {
	ctx := context.Background()
	e.deduped.Add(ctx, 0)
	e.unknownType.Add(ctx, 0)
	e.enqueueRejected.Add(ctx, 0)
}

// gateLabel returns the label value for a gate TYPE, folding TYPEs beyond
// maxGateTypeLabels into GateLabelOther so an arbitrary TYPE vocabulary cannot
// blow up the series count.
func (e *Emitter) gateLabel(t string) string {
	e.gateMu.Lock()
	defer e.gateMu.Unlock()
	if _, ok := e.gateSeen[t]; ok {
		return t
	}
	if len(e.gateSeen) >= maxGateTypeLabels {
		return GateLabelOther
	}
	e.gateSeen[t] = struct{}{}
	return t
}

// OnGateSet implements eventqueue.GateObserver.
func (e *Emitter) OnGateSet(gateType string, renewal bool) {
	r := "false"
	if renewal {
		r = "true"
	}
	e.gateSets.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("type", e.gateLabel(gateType)), attribute.String("renewal", r),
	))
}

// OnGateCleared implements eventqueue.GateObserver.
func (e *Emitter) OnGateCleared(gateType string, held time.Duration) {
	l := e.gateLabel(gateType)
	e.gateClears.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", l)))
	e.gateDuration.Record(context.Background(), held.Seconds(), metric.WithAttributes(
		attribute.String("type", l), attribute.String("outcome", "cleared"),
	))
}

// OnGateExpired implements eventqueue.GateObserver.
func (e *Emitter) OnGateExpired(gateType string, held time.Duration) {
	l := e.gateLabel(gateType)
	e.gateExpiries.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", l)))
	e.gateDuration.Record(context.Background(), held.Seconds(), metric.WithAttributes(
		attribute.String("type", l), attribute.String("outcome", "expired"),
	))
}

// OnGateBlocked implements eventqueue.GateObserver.
func (e *Emitter) OnGateBlocked(participant, kind, gateType string) {
	e.gateBlocked.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("participant", participant),
		attribute.String("kind", kind),
		attribute.String("type", e.gateLabel(gateType)),
	))
}

// OnGateDrop implements eventqueue.GateObserver.
func (e *Emitter) OnGateDrop(evtType, listenerID, gateType string) {
	e.gateDrops.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("event_type", evtType),
		attribute.String("role", listenerID),
		attribute.String("gate", e.gateLabel(gateType)),
	))
}

// OnEnqueue records evt's type and resolved enqueue instant, keyed by evt.ID,
// for OnAccept to consult (RecordThroughput/RecordDispatchLatency's
// production call site — see their own docs). Queue depth itself is
// observed via the gauge callback (reading the live depth), not pushed per
// event; this bookkeeping exists purely to recover what OnAccept's signature
// does not carry.
//
// Mirrors cmd/pg-router/run.go's activityObserver.OnEnqueue, including the
// FIFO-at-cap eviction: an event that is only ever declined-then-expired
// (never accepted) is never removed from this map by anything OTHER than
// that eviction, so dispatchPendingCap bounds it the same way
// activityPendingTypesCap bounds its sibling.
//
// An already-present id is REFRESHED in place rather than skipped: a fresh
// event legitimately reusing an id after its predecessor's retention ended
// (eventqueue.Queue.Enqueue's documented stale-retire re-emit path,
// INV-EVT-3) must not be judged against the stale entry's type or enqueue
// time. `order` is left untouched on a refresh — the id's FIFO eviction
// position stays where it was first inserted, which only affects WHEN it
// might later be evicted, never correctness (a miss is the tolerated
// imperfection OnAccept's own doc describes).
func (e *Emitter) OnEnqueue(evt eventqueue.Event) {
	e.seedPending(evt)
}

// OnRestore implements eventqueue.RestoreObserver (bead pg2-0efop): it seeds
// the SAME eventID->{type, enqueue-time} correlation OnEnqueue does, for each
// event Queue.New's replay restored from the durable queue after a restart, so
// OnAccept counts RecordThroughput/RecordDispatchLatency for it instead of
// silently skipping it (the restart-blind defect behind the 2026-09-30
// pr.changed throughput-zero window). It records nothing else: no counter is
// emitted, because the event was already reported by the process that enqueued
// it. Dispatch latency for a restored event is measured from evt.At — the
// ORIGINAL resolved enqueue instant the durable record carries — so it
// includes the time spent queued across the restart, exactly what a live
// event's latency means.
func (e *Emitter) OnRestore(evt eventqueue.Event) {
	e.seedPending(evt)
}

// seedPending is the shared body of OnEnqueue and OnRestore.
func (e *Emitter) seedPending(evt eventqueue.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.pending[evt.ID]; exists {
		e.pending[evt.ID] = pendingDispatch{typ: evt.Type, enqueuedAt: evt.At}
		return
	}
	if len(e.order) >= dispatchPendingCap {
		oldest := e.order[0]
		e.order = e.order[1:]
		delete(e.pending, oldest)
	}
	e.pending[evt.ID] = pendingDispatch{typ: evt.Type, enqueuedAt: evt.At}
	e.order = append(e.order, evt.ID)
}

// OnAccept feeds RecordThroughput and RecordDispatchLatency from the entry
// OnEnqueue recorded for eventID. The entry is NOT deleted: Register's own
// doc says "fan-out delivers a matching event to each bound listener", so
// OnAccept can fire more than once for the SAME eventID in one Dispatch pass
// (once per accepting listener) — each such accept is a real, independent
// delivery (mirrored by queue.go's own per-listener q.delivered tally) and
// must be counted, so the entry must still be there for a second, third,
// etc. accept. It is removed only by dispatchPendingCap's FIFO eviction —
// the same lifecycle activityObserver's identical map already has. A miss
// there (evicted under load) is silently skipped: the same tolerated
// imperfection activityObserver's own doc accepts for its map.
// listenerID is the role name (orchestrator roleListener.ID()); it is recorded
// as the config-bounded "role" label on both series (bead pg2-nimab).
func (e *Emitter) OnAccept(eventID, listenerID string) {
	e.mu.Lock()
	p, ok := e.pending[eventID]
	e.mu.Unlock()
	if !ok {
		return
	}
	e.RecordThroughput(p.typ, listenerID)
	// Divide as a float rather than truncate through time.Duration.Milliseconds
	// (int64): that would floor 0.6ms to 0 and 4.9ms to 4, biasing every
	// sample low against the histogram's own sub-10ms buckets.
	elapsedMS := float64(e.now().Sub(p.enqueuedAt)) / float64(time.Millisecond)
	e.RecordDispatchLatency(elapsedMS, "accepted", listenerID, p.typ)
}

// OnAcceptTiming implements eventqueue.TimingObserver (bead pg2-n7da9): it feeds
// the queue-wait and run-time histograms for one accepted dispatch, per role (the
// listener id) and FULL event type. Unlike OnAccept it needs no eventID
// correlation entry: the timing carries the type and both instants itself, so an
// event evicted from (or never seeded into) the correlation map is still measured.
func (e *Emitter) OnAcceptTiming(t eventqueue.DispatchTiming) {
	e.RecordQueueWait(t.Wait(), t.ListenerID, t.EventType)
	e.RecordRun(t.Run(), t.ListenerID, t.EventType)
}

// timingBuckets are the explicit bucket upper bounds, in seconds, of both the
// queue-wait and the run-time histogram: sub-second to two hours. The same set
// serves both so one panel can overlay them; a wait longer than two hours falls in
// the overflow bucket.
var timingBuckets = []float64{0.1, 0.5, 1, 2.5, 5, 10, 20, 30, 45, 60, 120, 300, 600, 1200, 1800, 3600, 7200}

// RecordQueueWait records how long an accepted event waited before role's offer
// went in-flight. evtType is the RAW event type; this method bounds it with
// eventTypeLabel. Exported for direct/test use; the production call site is
// OnAcceptTiming.
func (e *Emitter) RecordQueueWait(wait time.Duration, role, evtType string) {
	e.queueWait.Record(context.Background(), wait.Seconds(), metric.WithAttributes(
		attribute.String("role", role),
		attribute.String("type", e.eventTypeLabel(evtType)),
	))
}

// RecordRun records one accepted dispatch's synchronous handler run time; see
// RecordQueueWait for the labels.
func (e *Emitter) RecordRun(run time.Duration, role, evtType string) {
	e.run.Record(context.Background(), run.Seconds(), metric.WithAttributes(
		attribute.String("role", role),
		attribute.String("type", e.eventTypeLabel(evtType)),
	))
}

// maxEventTypeLabels bounds how many distinct full event-type label values the
// wait/run histograms will mint; UnknownEntityType collects the rest.
const maxEventTypeLabels = 64

// maxEventTypeLen caps the length of a full event-type label value.
const maxEventTypeLen = 64

// eventTypeLabel returns the label value for a FULL event type ("pr.changed",
// not the entity prefix EntityType reduces it to — the two verbs are different
// workloads, and telling them apart is the point of the wait/run split).
//
// Cardinality bound (bead pg2-n7da9), the same shape as EntityType's: the value
// set is fixed by configuration, never by traffic. Event types are
// config-bounded (core.Ingest rejects any type no binding declares). As a
// defence against a type that is not a short identifier (a restored durable
// row from an older config, a malformed type) anything not matching
// [a-z][a-z0-9_.-]* of at most maxEventTypeLen bytes maps to the single fallback
// UnknownEntityType, and at most maxEventTypeLabels distinct values are minted
// per process, the rest folding into the same fallback.
func (e *Emitter) eventTypeLabel(evtType string) string {
	if !validEventTypeLabel(evtType) {
		return UnknownEntityType
	}
	e.typeMu.Lock()
	defer e.typeMu.Unlock()
	if _, ok := e.typeSeen[evtType]; ok {
		return evtType
	}
	if len(e.typeSeen) >= maxEventTypeLabels {
		return UnknownEntityType
	}
	e.typeSeen[evtType] = struct{}{}
	return evtType
}

func validEventTypeLabel(t string) bool {
	if t == "" || len(t) > maxEventTypeLen {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// OnUnconsumedExpired increments the unconsumed-expired counter for the event's
// type — the concrete "no event misses" signal (INV-DISP-3), fired from the
// queue's expiry-sweep path.
func (e *Emitter) OnUnconsumedExpired(evtType string) {
	e.unconsumed.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// OnDeclined feeds the failure-rate counter from the queue's Dispatch path
// (eventqueue.Observer): a graceful pre-accept decline, one of the two
// delivery-side cases INV-FAIL-1 covers (the other, OnDispatchFailure below,
// fires from the same Dispatch pass for the OTHER class). evtType (widened in
// Task 2.3) is accepted for interface symmetry with the queue's other per-type
// hooks but is not part of the failure-rate label set; listenerID is the role
// name and IS recorded as the config-bounded "role" label (bead pg2-nimab,
// DEC-OBS-5) — the counter's "class" dimension is FailureClassDeclined, the
// one class knowable at this call site.
//
// reason (also widened in Task 2.3) WAS likewise discarded — that task's own
// doc named this "metrics-catalog growth, out of this task's scope" rather
// than forbidding it. Bead pg2-j4uwg is that growth: reason is now recorded
// verbatim as a SECOND, additive "reason" label on the SAME counter/class
// (never a new class — INV-OBS-1's "exactly two" delivery-side failure
// classes is unaffected), opaque to this package exactly like
// eventqueue.OfferResult.DeclineDetail's own doc describes — it is
// DeclineReason's coarse text ("busy"/"unavailable"/"none") by default, or a
// Listener-supplied detail when one was given (e.g. pg-router-ccpool-
// handler's "at-capacity" / "capacity-unknown", forwarded through
// orchestrator.roleListener.Offer). This is what makes
// pg_router_failures_total{class="declined"} distinguishable by reason
// rather than a single undifferentiated bucket.
func (e *Emitter) OnDeclined(_, listenerID, reason string) {
	e.failures.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("class", FailureClassDeclined),
		attribute.String("role", listenerID),
		attribute.String("reason", reason),
	))
}

// OnDispatchFailure feeds the SAME failure-rate counter as OnDeclined, from
// the SAME queue Dispatch path (eventqueue.Observer), but labeled with the
// OTHER delivery-side class (INV-OBS-1): an outright dispatch failure where
// the core could not hand the event over at all — currently, a panic
// recovered from a Listener's Offer implementation (see
// eventqueue.Queue's offerSafely). evtType is accepted for the same interface-
// symmetry reason OnDeclined's doc gives and is likewise not part of the
// label set; the class dimension is FailureClassDispatchFail and listenerID
// (the role name) is the config-bounded "role" label (bead pg2-nimab).
func (e *Emitter) OnDispatchFailure(_, listenerID string) {
	e.failures.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("class", FailureClassDispatchFail),
		attribute.String("role", listenerID),
	))
}

// OnHandlerFailure implements orchestrator.HandlerFailureObserver
// (structurally — this package deliberately does not import
// internal/orchestrator, the same decoupling eventqueue.Observer/
// core.IngestObserver/discover.SourceFailureObserver already follow here).
// It feeds the SAME failure-rate counter as OnDeclined/OnDispatchFailure,
// labeled with the THIRD delivery-side class (bead pg2-97539): a genuine,
// non-panic error the handler itself reported through roleListener.Offer's
// synchronous wireclient.Dispatch call, a case neither OnDeclined nor
// OnDispatchFailure's own call sites can ever observe (see
// FailureClassHandlerError's own doc for why). eventID/evtType are accepted
// for the same interface-symmetry reason OnDeclined/OnDispatchFailure's docs
// give and are likewise not part of the label set; the class dimension is
// FailureClassHandlerError. listenerID (this task, widening the interface
// for a per-role handler-failure tally) is likewise not part of this
// pool-wide metric's label set, so it is accepted and ignored here too.
//
// Reason (pg2-irowq, pg2-u2yub): a triage role's failures carry
// reason=ReasonTriagerFailure (bulkhead, wins over every other reason); else a
// budget-stop sentinel carries reason=ReasonBudgetExceeded; else no reason.
func (e *Emitter) OnHandlerFailure(_, _, listenerID string, err error) {
	attrs := []attribute.KeyValue{
		attribute.String("class", FailureClassHandlerError),
		attribute.String("role", listenerID),
	}
	switch {
	case IsTriagerRole(listenerID):
		// Bulkhead (pg2-u2yub): triager failures never share the worker/
		// review failure-rate series, whatever their cause.
		attrs = append(attrs, attribute.String("reason", ReasonTriagerFailure))
	case err != nil && strings.Contains(err.Error(), BudgetExceededSentinel):
		attrs = append(attrs, attribute.String("reason", ReasonBudgetExceeded))
	}
	e.failures.Add(context.Background(), 1, metric.WithAttributes(attrs...))
}

// OnDispatchRetry implements orchestrator.DispatchRetryObserver
// (structurally, like OnHandlerFailure): it counts one re-run scheduled for a
// transiently failed dispatch, labeled by the config-bounded role name and the
// transient class (bead pg2-yu5y2).
func (e *Emitter) OnDispatchRetry(role, class string) {
	e.dispatchRetries.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("role", role),
		attribute.String("class", class),
	))
}

// OnDeduped implements both the extended core.IngestObserver contract AND
// (Task 2.3, pg2-84o3m.22) eventqueue.Observer's identically-named
// `OnDeduped(evtType string)` — one method necessarily answers both, since
// Go resolves interface satisfaction structurally. It increments the deduped
// counter, per type, when a duplicate id still retained in the queue
// (INV-EVT-3, bead pg2-cz31d) is absorbed — the metrics half of the Debug
// log line internal/core/ingest.go's handleIngestEvent already writes at
// that same res == eventqueue.Deduped branch. eventqueue.Queue.Enqueue's OWN
// OnDeduped call (fired for every producer, not just core's ingest path) is
// deliberately NOT ALSO routed here for an ingest-driven dedup — see
// cmd/pg-router/run.go's fanOutObserver.OnDeduped — so this counter is not
// double-incremented for the one event both hooks can see.
func (e *Emitter) OnDeduped(evtType string) {
	e.deduped.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// OnEnqueueRejected implements eventqueue.RejectObserver: it counts one event
// admission the queue refused because the event log is at its hard limit
// (reason "log_full") or unwritable ("log_unwritable"), per event type.
func (e *Emitter) OnEnqueueRejected(evtType, reason string) {
	e.enqueueRejected.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("type", evtType), attribute.String("reason", reason),
	))
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
// failure class (INV-FAIL-1) — a pre-accept decline, a dispatch failure
// where pg-router could not hand the event over at all, or (bead pg2-97539)
// a non-panic error the handler itself reported. It is KEPT as the
// production entry point (OnDeclined calls it with FailureClassDeclined,
// OnDispatchFailure with FailureClassDispatchFail, OnHandlerFailure with
// FailureClassHandlerError) so the instrument itself, and any future
// delivery-side class, has one place to land; it is exported for direct use
// in tests.
//
// It is scoped DOWN, not repurposed: retryable / critical — everything
// POST-accept — is out of pg-router's measurement scope (operator scope-cut
// 2026-07-28) and MUST NOT be recorded here. There is no session-status
// callback to feed those classes from; internal/core dropped it outright (see
// that package's doc) because nothing in pg-router consumed a post-accept
// outcome.
//
// Operator ruling (Phillip, 2026-09-30, pg2-irowq) SUPERSEDES the earlier
// blanket exclusion for ONE resource-limit case: a handler error carrying the
// documented BudgetExceededSentinel is recorded under FailureClassHandlerError
// with reason=ReasonBudgetExceeded and a config-bounded role label (see
// OnHandlerFailure), so an operator can tell which role tripped its budget.
// It rides the handler-error text the core already receives; it is NOT a new
// session-status callback. Pool/limit/used/cap detail stays in the event text
// and the handler's eventlog, never in labels (ADR 0057).
func (e *Emitter) RecordFailure(class string) {
	e.failures.Add(context.Background(), 1, metric.WithAttributes(attribute.String("class", class)))
}

// Source-failure reasons: the closed set of values the "reason" label of
// MetricSourceFailures takes (bead pg2-hsla6). The label exists so an operator
// can tell a throttled GitHub budget from a dead backend from a daemon
// shutdown without reading logs; the alert pg-router-source-failure-rate
// aggregates it away (`sum by (source)`), so EVERY reason counts toward it.
const (
	// SourceFailureRateLimited: the backend refused because an upstream API
	// budget is exhausted or below its configured reserve (e.g. pg-connector-pr-github's
	// "GraphQL rate limit remaining (N) is below the configured reserve (M)").
	SourceFailureRateLimited = "rate-limited"
	// SourceFailureUnauthenticated: the backend reported scriptout "unauthenticated".
	SourceFailureUnauthenticated = "unauthenticated"
	// SourceFailureUnavailable: the backend reported scriptout "unavailable"
	// for any reason other than a rate limit.
	SourceFailureUnavailable = "unavailable"
	// SourceFailureTimeout: the attempt ran out its time budget and the child
	// was killed (the backend's 30s scriptout timeout, a SIGKILL, or a deadline
	// exceeded). Split out of "interrupted" by bead pg2-zdowv (DEC-OBS-8): it is
	// a genuine source problem, where a router-shutdown cancellation is not.
	SourceFailureTimeout = "timeout"
	// SourceFailureInterrupted: the attempt was cut short by a cancellation
	// that is NOT the router's own shutdown (that case is not a failure at all,
	// DEC-OBS-8) — e.g. a "context canceled" a backend reported from inside its
	// own process tree.
	SourceFailureInterrupted = "interrupted"
	// SourceFailureError: any other failure (non-zero exit with no
	// recognized backend class, malformed output, ...).
	SourceFailureError = "error"
)

// ClassifySourceFailure maps a pull-source query error onto one of the closed
// SourceFailure* reasons. The command query surfaces the backend's stderr
// inside the error text (the process boundary hides any errors.Is sentinel),
// so the backend classes are recognized by the pg-connector scriptout wire
// vocabulary ("scriptout: unavailable", "scriptout: unauthenticated") and the
// rate-limit phrasing, case-insensitively. Rate limiting is checked FIRST
// because a rate-limit breach also carries the generic "scriptout: unavailable"
// wrapper. A kill or deadline (SourceFailureTimeout) is checked before the
// generic "unavailable" wrapper too, so a backend that reports its own timeout
// as unavailable is still named a timeout. A nil error classifies as
// SourceFailureError.
//
// A router-shutdown cancellation never reaches here: discover.runAndEnqueue
// does not report it to OnSourceFailure at all (DEC-OBS-8).
func ClassifySourceFailure(err error) string {
	if err == nil {
		return SourceFailureError
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "below the configured reserve") || strings.Contains(msg, "rate limit"):
		return SourceFailureRateLimited
	case strings.Contains(msg, "scriptout: unauthenticated"):
		return SourceFailureUnauthenticated
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "signal: killed") || strings.Contains(msg, "deadline exceeded"):
		return SourceFailureTimeout
	case strings.Contains(msg, "scriptout: unavailable"):
		return SourceFailureUnavailable
	case errors.Is(err, context.Canceled) || strings.Contains(msg, "context canceled"):
		return SourceFailureInterrupted
	}
	return SourceFailureError
}

// registerSourceGauges seeds srcLastSuccess with start for every source whose
// interval is positive and registers the two observable gauges reading it.
func (e *Emitter) registerSourceGauges(m metric.Meter, sources map[string]time.Duration, start time.Time) error {
	intervals := make(map[string]time.Duration, len(sources))
	e.srcLastSuccess = make(map[string]time.Time, len(sources))
	for name, iv := range sources {
		if iv <= 0 {
			continue
		}
		intervals[name] = iv
		e.srcLastSuccess[name] = start
	}
	if _, err := m.Float64ObservableGauge(
		MetricSourceLastSuccess,
		metric.WithUnit("s"),
		metric.WithDescription("Unix time of each pull source's last successful pass (a gate- or halt-paused pass also advances it; a failed pass does not); starts at process start (DEC-OBS-4)"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			e.srcMu.Lock()
			defer e.srcMu.Unlock()
			for name, at := range e.srcLastSuccess {
				o.Observe(float64(at.UnixNano())/1e9, metric.WithAttributes(attribute.String("source", name)))
			}
			return nil
		}),
	); err != nil {
		return err
	}
	_, err := m.Float64ObservableGauge(
		MetricSourceExpectedInterval,
		metric.WithUnit("s"),
		metric.WithDescription("each pull source's configured period in seconds, the base of the persistent-failure alert threshold (DEC-OBS-4)"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			for name, iv := range intervals {
				o.Observe(iv.Seconds(), metric.WithAttributes(attribute.String("source", name)))
			}
			return nil
		}),
	)
	return err
}

// OnSourceSucceeded implements discover.SourceFailureObserver: it advances the
// source's last-success gauge to now. A source not registered via
// WithPullSources is ignored.
func (e *Emitter) OnSourceSucceeded(source string) { e.advanceSource(source) }

// OnSourcePaused implements discover.SourceFailureObserver: a pass skipped
// because a gate blocks the source or the log-size limit halted the emitters is
// a deliberate pause, not a failure, so it advances the gauge exactly like a
// success (DEC-OBS-4). Without this, a 60-minute pause would make every pull
// source look as if it had stopped succeeding.
func (e *Emitter) OnSourcePaused(source string) { e.advanceSource(source) }

func (e *Emitter) advanceSource(source string) {
	e.srcMu.Lock()
	defer e.srcMu.Unlock()
	if _, ok := e.srcLastSuccess[source]; !ok {
		return
	}
	e.srcLastSuccess[source] = e.now()
}

// sourceDurationBuckets are the pg_router_source_duration_seconds boundaries:
// sub-second to the 30s scriptout timeout (30 and 45 straddle it) and beyond.
var sourceDurationBuckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 45, 60, 120, 300}

// registerSourceTiming registers the attempt-duration histogram and the
// in-flight children gauge (bead pg2-zdowv, DEC-OBS-8). sources seeds the
// gauge so each pull source has a 0 series from start.
func (e *Emitter) registerSourceTiming(m metric.Meter, sources map[string]time.Duration) error {
	for name, iv := range sources {
		if iv > 0 {
			e.srcInflight[name] = 0
		}
	}
	h, err := m.Float64Histogram(
		MetricSourceDuration,
		metric.WithUnit("s"),
		metric.WithDescription("how long one pull-source query attempt ran, per source, success or failure; attempts cut short by router shutdown are not recorded (DEC-OBS-8)"),
		metric.WithExplicitBucketBoundaries(sourceDurationBuckets...),
	)
	if err != nil {
		return err
	}
	e.srcDuration = h
	_, err = m.Int64ObservableGauge(
		MetricSourceInflightChildren,
		metric.WithUnit("{process}"),
		metric.WithDescription("pull-source query attempts (child processes) currently running, per source (DEC-OBS-8)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			e.srcMu.Lock()
			defer e.srcMu.Unlock()
			for name, n := range e.srcInflight {
				o.Observe(int64(n), metric.WithAttributes(attribute.String("source", name)))
			}
			return nil
		}),
	)
	return err
}

// OnSourceAttemptStart implements discover.SourceFailureObserver: one more
// child process is running for source.
func (e *Emitter) OnSourceAttemptStart(source string) {
	e.srcMu.Lock()
	defer e.srcMu.Unlock()
	e.srcInflight[source]++
}

// OnSourceAttemptEnd implements discover.SourceFailureObserver: the child for
// source has ended. elapsed is recorded in the duration histogram unless the
// attempt was cut short by the router's own shutdown (its truncated duration
// says nothing about the source).
func (e *Emitter) OnSourceAttemptEnd(source string, elapsed time.Duration, shutdown bool) {
	e.srcMu.Lock()
	if e.srcInflight[source] > 0 {
		e.srcInflight[source]--
	}
	e.srcMu.Unlock()
	if shutdown {
		return
	}
	e.srcDuration.Record(context.Background(), elapsed.Seconds(),
		metric.WithAttributes(attribute.String("source", source)))
}

// OnSourceFailure implements discover.SourceFailureObserver (the interface is
// defined in internal/discover to keep the dependency direction the queue's
// own Observer already uses: the producer side declares the hook, metrics
// implements it). It increments the source-failures counter for a pull
// source whose query attempt failed — whether it will be retried after
// backoff or is the final give-up attempt (INV-FAIL-3, register gap R21 /
// beads pg2-00jpn, pg2-jgbnp) — the metrics half of the log lines
// discover.go's runAndEnqueue writes at those points. The series carries the
// ClassifySourceFailure reason of err (bead pg2-hsla6).
func (e *Emitter) OnSourceFailure(source string, err error) {
	e.sourceFailures.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("source", source),
		attribute.String("reason", ClassifySourceFailure(err)),
	))
}

// RecordThroughput increments the throughput counter, per type and role, for an event
// dispatched and accepted (STORY-OBS-1). Exported for direct/test use; its
// production call site is OnAccept, fed from the pending map OnEnqueue
// populates — eventqueue.Observer.OnAccept's signature carries (eventID,
// listenerID) only, not the event's type, so OnAccept recovers it the same
// way cmd/pg-router/run.go's activityObserver already does for the identical
// gap (see that type's own doc) rather than widening the Observer interface.
func (e *Emitter) RecordThroughput(evtType, role string) {
	e.throughput.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("type", evtType),
		attribute.String("role", role),
	))
}

// RecordDispatchLatency records the elapsed time, in milliseconds, from an
// event's enqueue to a settling dispatch outcome (STORY-OBS-1) — the
// catalog's one histogram, labeled by outcome, role (the accepting
// listener's role name, bead pg2-nimab) and type (the event's entity type,
// EntityType(evtType), bead pg2-sve9v). Exported for direct/test use;
// its production call site is OnAccept (see RecordThroughput's doc for the
// shared pending-map mechanics), which always passes "accepted" today — the
// one outcome OnAccept can observe. A future outcome (e.g. a final,
// terminal-settle decline or dispatch failure) needs
// eventqueue.Observer.OnDeclined/OnDispatchFailure to learn a terminal-settle
// flag first (they currently fire on every retry attempt, not only the one
// that settles the pair) — deliberately deferred (operator decision,
// 2026-09-18); labeling from the start means adding it later is a new call
// site, not another signature break.
//
// evtType is the RAW event type (e.g. "pr.changed"); this method reduces it
// with EntityType, so callers never pass a pre-reduced value and the label's
// bound is enforced in one place.
func (e *Emitter) RecordDispatchLatency(ms float64, outcome, role, evtType string) {
	e.dispatchLatency.Record(context.Background(), ms, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("role", role),
		attribute.String("type", EntityType(evtType)),
	))
}

// UnknownEntityType is the fixed fallback value of the dispatch-latency
// histogram's "type" label for an event type EntityType cannot reduce to an
// entity type (empty, a leading ".", or a prefix that is not a short
// lowercase identifier).
const UnknownEntityType = "other"

// maxEntityTypeLen caps the length of an entity-type label value. Real
// entity types are short words ("pr", "issue", "thread"); anything longer is
// not one.
const maxEntityTypeLen = 32

// EntityType reduces an event type to the entity type that is the
// dispatch-latency histogram's "type" label: the segment before the first
// ".", so "pr.changed" -> "pr", "pr.reconcile" -> "pr". An event type with
// no "." is its own entity type ("bead" -> "bead").
//
// Cardinality bound (bead pg2-sve9v): the label's value set is fixed by
// configuration, never by event traffic, for three reasons. (1) Event types
// are config-bounded: core.Ingest rejects any type no configured binding
// declares, so the set of types is declared in configuration. (2) Reducing to the
// first segment collapses the per-entity verbs (pr.new, pr.changed,
// pr.reconcile, ...) into one value per entity, so the set is at most the
// number of distinct entity prefixes in the config (pr, issue, thread, ...),
// no ids. (3) As a defence against a type that is not a short lowercase
// identifier (a restored durable-queue row from an older config, a malformed
// type), anything not matching [a-z][a-z0-9_-]* of at most maxEntityTypeLen
// bytes maps to the single fallback UnknownEntityType rather than becoming a
// new series. The raw, per-verb type stays available, unreduced, on the
// throughput counter's own "type" label.
func EntityType(evtType string) string {
	seg, _, _ := strings.Cut(evtType, ".")
	if seg == "" || len(seg) > maxEntityTypeLen {
		return UnknownEntityType
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return UnknownEntityType
		}
	}
	return seg
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
// query. cmd/pg-router's bootCore is expected to call this ONLY when no
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
// not a mandated backend") rather than a pg-router-specific shape: filtering
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

// registerLogLimitGauges registers the log-size limit's observable gauges over
// fn (see WithLogLimitStatus).
func registerLogLimitGauges(m metric.Meter, fn func() eventqueue.LimitStatus) error {
	if _, err := m.Int64ObservableGauge(
		MetricQueueLogLimitBytes,
		metric.WithUnit("By"),
		metric.WithDescription("the hard size limit of the event-queue write-ahead log (max_log_bytes); at or above it new events are rejected"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(fn().HardBytes)
			return nil
		}),
	); err != nil {
		return err
	}
	if _, err := m.Float64ObservableGauge(
		MetricQueueLogPercent,
		metric.WithUnit("%"),
		metric.WithDescription("size of the event-queue write-ahead log as a percentage (0-100, may exceed 100) of its hard limit"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			o.Observe(fn().Percent())
			return nil
		}),
	); err != nil {
		return err
	}
	if _, err := m.Int64ObservableGauge(
		MetricEmittersHalted,
		metric.WithUnit("{state}"),
		metric.WithDescription("1 while polled command-source emitters are not being polled because the event log is past its soft size threshold or unwritable, else 0; not a gate (timers, listeners and pushed events keep running)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			v := int64(0)
			if fn().EmittersHalted {
				v = 1
			}
			o.Observe(v)
			return nil
		}),
	); err != nil {
		return err
	}
	if _, err := m.Int64ObservableGauge(
		MetricLogRejecting,
		metric.WithUnit("{state}"),
		metric.WithDescription("1 per reason (log_full, log_unwritable) while the event log is in that state and refusing new events, else 0"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			st := fn().State
			for _, reason := range []string{eventqueue.ReasonLogFull, eventqueue.ReasonLogUnwritable} {
				v := int64(0)
				if st == reason {
					v = 1
				}
				o.Observe(v, metric.WithAttributes(attribute.String("reason", reason)))
			}
			return nil
		}),
	); err != nil {
		return err
	}
	return nil
}
