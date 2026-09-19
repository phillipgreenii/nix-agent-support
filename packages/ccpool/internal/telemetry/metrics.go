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
		metric.WithDescription("Sessions reap is currently preserving for human review (preservedForHuman)."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_sessions_preserved_for_human: %w", err)
	}
	if m.launchOutcomeTotal, err = meter.Int64Counter(
		"ccpool_launch_outcome_total",
		metric.WithDescription("Session-launch outcomes, labeled by route and outcome, recorded once the outcome is resolved (not at branch selection)."),
	); err != nil {
		return metricsInstruments{}, fmt.Errorf("ccpool_launch_outcome_total: %w", err)
	}

	return m, nil
}

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

// RecordRetry increments ccpool_retries_total, labeled by class. Call site:
// cmd/ccpool/retry.go's retryDecision/RetryClass (unchanged) — see the
// sibling call-sites packet.
func RecordRetry(class string) {
	i := ensureInstruments()
	if i.retriesTotal == nil {
		return
	}
	i.retriesTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("class", class)))
}

// RecordRetryExhausted increments ccpool_retry_exhausted_total. It carries no
// labels.
//
// This function does NOT decide whether genuine exhaustion has occurred —
// registration/no-op safety is this packet's own scope. The caller (the
// sibling call-sites packet's corrected maybeRetry logic) MUST invoke this
// ONLY at the two genuine-exhaustion returns retryDecision can produce (max
// attempts or retry-window timeout), and MUST NOT invoke it at a simple
// policy decline (disabled, or the class not configured) — retryDecision's
// bare `false` return does not by itself distinguish the two (design D6
// round-2 semantic post-check finding).
func RecordRetryExhausted() {
	i := ensureInstruments()
	if i.retryExhaustedTotal == nil {
		return
	}
	i.retryExhaustedTotal.Add(context.Background(), 1)
}

// RecordCancel increments ccpool_cancel_total, labeled by outcome. Call
// site: internal/session/cancel_close.go's Cancel success/
// ErrCancelUnconfirmed paths (unchanged) — see the sibling call-sites
// packet.
func RecordCancel(outcome string) {
	i := ensureInstruments()
	if i.cancelTotal == nil {
		return
	}
	i.cancelTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// RecordReapClosure increments ccpool_reap_closures_total, labeled by reason
// ("idle_ttl" or "cap_eviction"). Call site: internal/session/reap.go's
// Pass 1/Pass 2 (unchanged) — see the sibling call-sites packet.
func RecordReapClosure(reason string) {
	i := ensureInstruments()
	if i.reapClosuresTotal == nil {
		return
	}
	i.reapClosuresTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordReapPhantomPruned increments ccpool_reap_phantom_pruned_total. It
// carries no labels. Call site: internal/session/reap.go's Pass 0 (unchanged)
// — see the sibling call-sites packet.
func RecordReapPhantomPruned() {
	i := ensureInstruments()
	if i.reapPhantomPrunedTotal == nil {
		return
	}
	i.reapPhantomPrunedTotal.Add(context.Background(), 1)
}

// RecordSessionsPreservedForHuman records the current count of sessions reap
// is preserving for human review. It carries no labels.
//
// A SYNCHRONOUS gauge (Int64Gauge), not an observable/callback one:
// reap.go's preservedForHuman count is a single per-invocation snapshot
// (ccpool is a one-shot CLI, not a resident process to poll), so recording it
// directly at the point reap.go computes it — with no new polling loop or
// background tracking state (design D6/D4) — is exactly what a
// synchronous gauge is for. Call site: internal/session/reap.go's
// preservedForHuman (unchanged) — see the sibling call-sites packet.
func RecordSessionsPreservedForHuman(count int64) {
	i := ensureInstruments()
	if i.sessionsPreservedForHuman == nil {
		return
	}
	i.sessionsPreservedForHuman.Record(context.Background(), count)
}

// RecordLaunchOutcome increments ccpool_launch_outcome_total, labeled by
// route and outcome. route MUST be one of reuse_live|resume|
// resume_fresh_starting|brand_new|preflight; outcome MUST be one of
// success|timeout|error.
//
// REDEFINED per the design's round-1 semantic post-check finding: callers
// record this once the actual outcome is known — after launchAndWait
// resolves (success/timeout/tmux-error), at an early preflight-failure
// return (ErrNoPluginDir, trust failure, mcpconsent failure), or — for
// route=reuse_live specifically, which never reaches launchAndWait or a
// preflight-failure return — at ensureLocked's own trivial-success early
// return. Never at branch selection in ensureLocked. See the sibling
// call-sites packet.
func RecordLaunchOutcome(route, outcome string) {
	i := ensureInstruments()
	if i.launchOutcomeTotal == nil {
		return
	}
	i.launchOutcomeTotal.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("route", route), attribute.String("outcome", outcome)))
}
