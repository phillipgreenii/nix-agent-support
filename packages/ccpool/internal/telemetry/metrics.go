// metrics.go — ccpool-native metrics instrument catalog (pg2-qye99 /
// pg2-24f89, design D6). Defines and registers the seven event-driven
// counters/gauges the design specifies, against the SAME MeterProvider
// telemetry.Init installs (real or no-op) — see MeterProvider in
// telemetry.go. It does NOT add the increment call sites: those live in the
// sibling packet "ccpool: metric call sites + per-session log narration"
// (pg2-qye99 / pg2-24f89, design D6). In particular, RecordRetryExhausted
// increments unconditionally whenever called — it is the sibling packet's
// job to call it ONLY at the two genuine-exhaustion points (max attempts or
// retry-window timeout), never at a simple policy decline
// (disabled/unconfigured class); see its own doc comment below.
//
// Registration is LAZY (on first Record* call), not a package init(): a
// package-level var initializer runs before cmd/ccpool/main.go's run() calls
// telemetry.Init, so registering eagerly there would permanently bind these
// instruments to the placeholder no-op MeterProvider that predates Init,
// never seeing the real provider Init installs when an OTLP endpoint is
// configured. By the time any call site invokes a Record* function, Init has
// already run (it is the first thing run() does, unconditionally, for every
// subcommand) — so first-use registration reliably observes whichever
// provider Init installed for this invocation.
//
// Every record also carries a `pool` attribute plus an operator-allowlisted
// subset of the session's labels: see MetricAttrs, which is the single
// cardinality guard, and the `attrs` parameter every Record* function takes.
//
// No new polling, no new tracking state, no daemon mode — metrics stay
// push-based, flushed per invocation (design D6, D4).
package telemetry

import (
	"context"
	"fmt"
	"os"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metricsInstruments holds the seven D6 instrument handles. The zero value
// (all nil fields) is a valid, fully safe state: every Record* function
// nil-checks its instrument before use, so a registration failure (see
// newMetricsInstruments) degrades to a silent no-op rather than a panic.
type metricsInstruments struct {
	retriesTotal              metric.Int64Counter
	retryExhaustedTotal       metric.Int64Counter
	cancelTotal               metric.Int64Counter
	reapClosuresTotal         metric.Int64Counter
	reapPhantomPrunedTotal    metric.Int64Counter
	sessionsPreservedForHuman metric.Int64Gauge
	launchOutcomeTotal        metric.Int64Counter
	sessionStates             metric.Int64Gauge
	sessionInfo               metric.Int64Gauge
	sessionDuration           metric.Float64Histogram
	sessionsClosedTotal       metric.Int64Counter
}

// newMetricsInstruments builds the seven instruments against meter, under
// exactly the names the design table specifies. A pure function (no package
// state), kept separate from the lazy singleton below so tests can exercise
// registration directly against a fresh, test-local meter — see
// metrics_test.go.
func newMetricsInstruments(meter metric.Meter) (metricsInstruments, error) {
	var m metricsInstruments
	var err error

	if m.retriesTotal, err = meter.Int64Counter(
		"ccpool_retries_total",
		metric.WithDescription("Retry attempts, labeled by class."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_retries_total: %w", err)
	}
	if m.retryExhaustedTotal, err = meter.Int64Counter(
		"ccpool_retry_exhausted_total",
		metric.WithDescription("Genuine retry-policy exhaustion (max attempts or retry-window timeout) only — never a simple disabled/unconfigured policy decline."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_retry_exhausted_total: %w", err)
	}
	if m.cancelTotal, err = meter.Int64Counter(
		"ccpool_cancel_total",
		metric.WithDescription("Session cancel outcomes, labeled by outcome."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_cancel_total: %w", err)
	}
	if m.reapClosuresTotal, err = meter.Int64Counter(
		"ccpool_reap_closures_total",
		metric.WithDescription("Sessions closed by reap, labeled by reason (idle_ttl|cap_eviction)."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_reap_closures_total: %w", err)
	}
	if m.reapPhantomPrunedTotal, err = meter.Int64Counter(
		"ccpool_reap_phantom_pruned_total",
		metric.WithDescription("Phantom session records pruned by reap Pass 0."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_reap_phantom_pruned_total: %w", err)
	}
	if m.sessionsPreservedForHuman, err = meter.Int64Gauge(
		"ccpool_sessions_preserved_for_human",
		metric.WithDescription("Sessions reap is currently preserving for human review (preservedForHuman), one value per pool (and allowlisted label set). Each short-lived ccpool invocation pushes its own snapshot, so the series is last-value-wins: the most recent reap's count is what shows."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_sessions_preserved_for_human: %w", err)
	}
	if m.launchOutcomeTotal, err = meter.Int64Counter(
		"ccpool_launch_outcome_total",
		metric.WithDescription("Session-launch outcomes, labeled by route and outcome, recorded once the outcome is resolved (not at branch selection)."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_launch_outcome_total: %w", err)
	}
	if m.sessionStates, err = meter.Int64Gauge(
		"ccpool_session_states",
		metric.WithDescription("Sessions in the registry by store state and tmux liveness (live=true|false), snapshotted at each reap. live=false,state=working is dead-but-working; live=true,state=needs_input is a stuck/parked session."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_session_states: %w", err)
	}
	if m.sessionInfo, err = meter.Int64Gauge(
		"ccpool_session_info",
		metric.WithDescription("Info gauge, value 1 per live session: maps claude_session_id to pool (and allowlisted labels such as pgrouter.role), so a dashboard can join pa-monitor's per-session series to a pool. Emitted by each reap for live sessions only; a series goes stale once its session ends or is pruned."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_session_info: %w", err)
	}
	if m.sessionDuration, err = meter.Float64Histogram(
		"ccpool_session_duration_seconds",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of one session RUN (a launch or resume), ended_at - started_at from session_runs, labeled by result (the close reason: idle_ttl|cap_eviction|operator|handler|exited). A resumed session's closed gap is not counted."),
		metric.WithExplicitBucketBoundaries(SessionDurationBuckets...),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_session_duration_seconds: %w", err)
	}
	if m.sessionsClosedTotal, err = meter.Int64Counter(
		"ccpool_sessions_closed_total",
		metric.WithDescription("Session runs that ended, labeled by result (the close reason: idle_ttl|cap_eviction|operator|handler|exited)."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_sessions_closed_total: %w", err)
	}

	return m, nil
}

// SessionDurationBuckets are the explicit histogram boundaries (seconds) for
// ccpool_session_duration_seconds, spanning 30 s to 4 h; the SDK default
// boundaries are tuned for milliseconds and are wrong for run durations.
var SessionDurationBuckets = []float64{30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400}

// ResultAttrKey is the attribute carrying a run's close reason on the run
// lifecycle instruments.
const ResultAttrKey = "result"

// instrumentsOnce/liveInstruments back the lazy singleton ensureInstruments
// registers exactly once, on first use, against MeterProvider().Meter(...) —
// see this file's package doc for why lazy-on-first-use (not package init())
// is required for correctness.
var (
	instrumentsOnce sync.Once
	liveInstruments metricsInstruments
)

// ensureInstruments registers the package's metrics instruments against
// MeterProvider() exactly once and returns the (possibly all-nil, on
// failure) handles. Safe to call from any Record* function at any time,
// including before telemetry.Init has run — MeterProvider() itself is
// nil-safe and returns a usable no-op provider in that case.
func ensureInstruments() metricsInstruments {
	instrumentsOnce.Do(func() {
		m, err := newMetricsInstruments(MeterProvider().Meter(scopeName))
		if err != nil {
			// Mirrors Init's own "log one stderr warning, degrade to no-op"
			// style. Unreachable in practice with these static, valid,
			// non-duplicated instrument names; kept so a future edit that
			// breaks that invariant fails loudly instead of panicking every
			// Record* call site.
			fmt.Fprintf(os.Stderr, "ccpool: metrics instrument registration failed (%v); metrics disabled\n", err)
			return
		}
		liveInstruments = m
	})
	return liveInstruments
}

// PoolAttrKey is the metric attribute key carrying a record's pool. Its value
// is ALWAYS the basename of the resolved pool directory (PoolName), or the
// literal DefaultPoolName in default mode — never a path. The Prometheus label
// is the same string (no dots, so no escaping).
const PoolAttrKey = "pool"

// MetricAttrs builds the attribute set every Record* function stamps on a
// record: the pool (PoolAttrKey = basename of poolRoot, or DefaultPoolName
// when poolRoot is empty) plus those of sessionAttrs whose key appears in
// allowlist.
//
// CARDINALITY GUARD. sessionAttrs is what SessionAttrs resolved and applies
// no key filtering of its own (it feeds logs too, where every label belongs).
// Metrics are different: every attribute multiplies the series count, so a
// session label reaches a metric ONLY if the operator allowlisted its key
// (ccpool config [telemetry] metric_label_allowlist; the default is
// pgrouter.role only). A nil or empty allowlist fails closed: pool
// only. Allowlist entries are the dotted Go/OTel keys, never the underscored
// Prometheus form. Keys not on the allowlist are dropped from metrics (they
// still appear on logs). Never allowlist an id-like or path-like label
// (external_id, session_id, bead ids, filesystem paths): it makes series
// cardinality unbounded.
//
// PoolAttrKey is reserved: a label (or allowlist entry) named "pool" is
// ignored, so a session can never spoof the pool. Labels come first and pool
// last in the returned slice; Record* functions append their own attributes
// after this set, and a later duplicate key wins, so an instrument's own
// attributes (class, outcome, ...) can never be displaced by a label.
//
// The pool is passed in EXPLICITLY (a per-Service / per-actuator value),
// never read from the CCPOOL_POOL environment: reap-all governs many pools in
// one process without mutating that variable. The returned slice is fresh and
// owned by the caller; sessionAttrs is not modified.
func MetricAttrs(poolRoot string, allowlist []string, sessionAttrs []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(sessionAttrs)+1)
	if len(allowlist) > 0 {
		allowed := make(map[attribute.Key]struct{}, len(allowlist))
		for _, k := range allowlist {
			allowed[attribute.Key(k)] = struct{}{}
		}
		for _, a := range sessionAttrs {
			if a.Key == PoolAttrKey {
				continue
			}
			if _, ok := allowed[a.Key]; ok {
				out = append(out, a)
			}
		}
	}
	return append(out, attribute.String(PoolAttrKey, PoolName(poolRoot)))
}

// withAttrs merges the caller-supplied base attributes (MetricAttrs output)
// with an instrument's own, into a fresh slice so the caller's slice is never
// aliased or mutated. The instrument's own attributes come last: on a
// duplicate key the later value wins (attribute.NewSet semantics).
func withAttrs(base []attribute.KeyValue, own ...attribute.KeyValue) metric.MeasurementOption {
	all := make([]attribute.KeyValue, 0, len(base)+len(own))
	all = append(all, base...)
	all = append(all, own...)
	return metric.WithAttributes(all...)
}

// The instrument methods below hold the recording logic so tests can drive
// it against a test-local meter (newMetricsInstruments); the exported Record*
// functions are thin wrappers over the lazy process-wide singleton. Every
// method nil-checks its instrument (the zero-value metricsInstruments is a
// safe no-op).

func (m metricsInstruments) retry(ctx context.Context, class string, attrs []attribute.KeyValue) {
	if m.retriesTotal == nil {
		return
	}
	m.retriesTotal.Add(ctx, 1, withAttrs(attrs, attribute.String("class", class)))
}

func (m metricsInstruments) retryExhausted(ctx context.Context, attrs []attribute.KeyValue) {
	if m.retryExhaustedTotal == nil {
		return
	}
	m.retryExhaustedTotal.Add(ctx, 1, withAttrs(attrs))
}

func (m metricsInstruments) cancel(ctx context.Context, outcome string, attrs []attribute.KeyValue) {
	if m.cancelTotal == nil {
		return
	}
	m.cancelTotal.Add(ctx, 1, withAttrs(attrs, attribute.String("outcome", outcome)))
}

func (m metricsInstruments) reapClosure(ctx context.Context, reason string, attrs []attribute.KeyValue) {
	if m.reapClosuresTotal == nil {
		return
	}
	m.reapClosuresTotal.Add(ctx, 1, withAttrs(attrs, attribute.String("reason", reason)))
}

func (m metricsInstruments) reapPhantomPruned(ctx context.Context, attrs []attribute.KeyValue) {
	if m.reapPhantomPrunedTotal == nil {
		return
	}
	m.reapPhantomPrunedTotal.Add(ctx, 1, withAttrs(attrs))
}

func (m metricsInstruments) sessionsPreserved(ctx context.Context, count int64, attrs []attribute.KeyValue) {
	if m.sessionsPreservedForHuman == nil {
		return
	}
	m.sessionsPreservedForHuman.Record(ctx, count, withAttrs(attrs))
}

func (m metricsInstruments) launchOutcome(ctx context.Context, route, outcome string, attrs []attribute.KeyValue) {
	if m.launchOutcomeTotal == nil {
		return
	}
	m.launchOutcomeTotal.Add(ctx, 1, withAttrs(attrs, attribute.String("route", route), attribute.String("outcome", outcome)))
}

func (m metricsInstruments) states(ctx context.Context, counts []SessionStateCount, attrs []attribute.KeyValue) {
	if m.sessionStates == nil {
		return
	}
	for _, c := range counts {
		m.sessionStates.Record(ctx, c.Count, withAttrs(attrs, attribute.String("state", c.State), attribute.Bool("live", c.Live)))
	}
}

func (m metricsInstruments) info(ctx context.Context, claudeSessionID string, attrs []attribute.KeyValue) {
	if m.sessionInfo == nil || claudeSessionID == "" {
		return
	}
	m.sessionInfo.Record(ctx, 1, withAttrs(attrs, attribute.String(ClaudeSessionIDAttrKey, claudeSessionID)))
}

func (m metricsInstruments) sessionClosed(ctx context.Context, durationSeconds float64, result string, attrs []attribute.KeyValue) {
	opt := withAttrs(attrs, attribute.String(ResultAttrKey, result))
	if m.sessionDuration != nil {
		m.sessionDuration.Record(ctx, durationSeconds, opt)
	}
	if m.sessionsClosedTotal != nil {
		m.sessionsClosedTotal.Add(ctx, 1, opt)
	}
}

// ClaudeSessionIDAttrKey is the attribute key carrying a Claude session id on
// ccpool_session_info, and on that gauge ONLY.
//
// EXCEPTION to the cardinality rule (packet A / design 3 P1): metric
// attributes MUST NOT carry per-session ids (external_id, session_id, bead
// ids, paths). This one is permitted because the gauge is bounded by LIVE
// sessions (the same order as pa-monitor's own pa_monitor_session_info) and
// exists solely to join pa-monitor's session series to a pool on the Claude
// session id. Never add it to another instrument or to the label allowlist.
const ClaudeSessionIDAttrKey = "claude_session_id"

// Every Record* function below takes the record's attribute set as its LAST
// parameter, `attrs []attribute.KeyValue` — the one form used by all of them,
// so later instruments follow the same style. Callers build it with
// MetricAttrs (pool + allowlisted session labels), resolving the labels
// BEFORE any store delete that would remove them. Instruments never read the
// pool from the environment and never filter labels themselves.

// RecordRetry increments ccpool_retries_total, labeled by class (plus attrs).
// Call site: cmd/ccpool/retry.go's maybeRetry.
func RecordRetry(class string, attrs []attribute.KeyValue) {
	ensureInstruments().retry(context.Background(), class, attrs)
}

// RecordRetryExhausted increments ccpool_retry_exhausted_total (labeled by
// attrs only).
//
// This function does NOT decide whether genuine exhaustion has occurred.
// The caller (maybeRetry) MUST invoke this ONLY at the two genuine-exhaustion
// returns retryDecision can produce (max attempts or retry-window timeout),
// and MUST NOT invoke it at a simple policy decline (disabled, or the class
// not configured) — retryDecision's bare `false` return does not by itself
// distinguish the two (design D6 round-2 semantic post-check finding).
func RecordRetryExhausted(attrs []attribute.KeyValue) {
	ensureInstruments().retryExhausted(context.Background(), attrs)
}

// RecordCancel increments ccpool_cancel_total, labeled by outcome (plus
// attrs). Call site: internal/session/cancel_close.go's Cancel success/
// ErrCancelUnconfirmed paths.
func RecordCancel(outcome string, attrs []attribute.KeyValue) {
	ensureInstruments().cancel(context.Background(), outcome, attrs)
}

// RecordReapClosure increments ccpool_reap_closures_total, labeled by reason
// ("idle_ttl" or "cap_eviction") plus attrs. Call site:
// internal/session/reap.go's Pass 1/Pass 2.
func RecordReapClosure(reason string, attrs []attribute.KeyValue) {
	ensureInstruments().reapClosure(context.Background(), reason, attrs)
}

// RecordReapPhantomPruned increments ccpool_reap_phantom_pruned_total
// (labeled by attrs only). Call site: internal/session/reap.go's Pass 0. The
// caller MUST resolve attrs BEFORE Store.Delete: the delete also removes the
// session's metadata, so labels resolved afterwards would always be empty.
func RecordReapPhantomPruned(attrs []attribute.KeyValue) {
	ensureInstruments().reapPhantomPruned(context.Background(), attrs)
}

// RecordSessionsPreservedForHuman records the current count of sessions reap
// is preserving for human review, for the (pool, label set) that attrs
// identifies — reap emits ONE value per pool and label set.
//
// A SYNCHRONOUS gauge (Int64Gauge), not an observable/callback one:
// reap.go's preservedForHuman count is a single per-invocation snapshot
// (ccpool is a one-shot CLI, not a resident process to poll), so recording it
// directly at the point reap.go computes it — with no new polling loop or
// background tracking state (design D6/D4) — is exactly what a
// synchronous gauge is for. Because each CLI invocation pushes its own
// snapshot, the series is last-value-wins across invocations (see the
// instrument's description).
func RecordSessionsPreservedForHuman(count int64, attrs []attribute.KeyValue) {
	ensureInstruments().sessionsPreserved(context.Background(), count, attrs)
}

// RecordLaunchOutcome increments ccpool_launch_outcome_total, labeled by
// route and outcome (plus attrs). route MUST be one of reuse_live|resume|
// resume_fresh_starting|brand_new|preflight; outcome MUST be one of
// success|timeout|error.
//
// REDEFINED per the design's round-1 semantic post-check finding: callers
// record this once the actual outcome is known — after launchAndWait
// resolves (success/timeout/tmux-error), at an early preflight-failure
// return (ErrNoPluginDir, trust failure, mcpconsent failure), or — for
// route=reuse_live specifically, which never reaches launchAndWait or a
// preflight-failure return — at ensureLocked's own trivial-success early
// return. Never at branch selection in ensureLocked.
func RecordLaunchOutcome(route, outcome string, attrs []attribute.KeyValue) {
	ensureInstruments().launchOutcome(context.Background(), route, outcome, attrs)
}

// SessionStateCount is one (state, live) bucket of the registry snapshot fed
// to RecordSessionStates.
type SessionStateCount struct {
	State string
	Live  bool
	Count int64
}

// RecordSessionStates records ccpool_session_states: one gauge point per
// supplied (state, live) bucket, each also carrying attrs (the pool; the
// snapshot is pool-wide so it carries no per-session labels). Cardinality is
// bounded (states x 2 per pool), and the caller MUST pass every bucket
// including zeros so a dashboard sees an explicit 0 rather than a stale prior
// value. Like RecordSessionsPreservedForHuman it is a per-invocation snapshot
// on a synchronous gauge: no polling, no tracking state.
func RecordSessionStates(counts []SessionStateCount, attrs []attribute.KeyValue) {
	ensureInstruments().states(context.Background(), counts, attrs)
}

// RecordSessionInfo records ccpool_session_info = 1 for one live session:
// claude_session_id (the documented cardinality exception, see
// ClaudeSessionIDAttrKey) plus attrs (pool and allowlisted labels). An empty
// claudeSessionID records nothing (nothing to join on). Synchronous gauge, one
// point per live session per reap; no tracking state.
func RecordSessionInfo(claudeSessionID string, attrs []attribute.KeyValue) {
	ensureInstruments().info(context.Background(), claudeSessionID, attrs)
}

// RecordSessionClosed records one ended session RUN: one observation on
// ccpool_session_duration_seconds (durationSeconds = ended_at - started_at of
// the run) and one increment of ccpool_sessions_closed_total, both labeled by
// result (the close reason: idle_ttl|cap_eviction|operator|handler|exited) plus
// attrs (pool and the allowlisted labels, resolved BEFORE any delete). The
// caller MUST invoke it at most once per run (the session_runs metrics_emitted
// claim guarantees that).
func RecordSessionClosed(durationSeconds float64, result string, attrs []attribute.KeyValue) {
	ensureInstruments().sessionClosed(context.Background(), durationSeconds, result, attrs)
}
