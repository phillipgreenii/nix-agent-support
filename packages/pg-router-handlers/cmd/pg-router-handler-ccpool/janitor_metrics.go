package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
)

// Heartbeat and orphan gauges (bead pg2-fx5x0l.7, INV-CCH-41). The handler gains
// no OTel: each gauge goes through the generic `ccpool emit-gauge` command
// (bead pg2-fx5x0l.8), which exports it over OTLP from the inherited OTEL_* env
// and is a silent no-op without one. The names live under ccpool_ because that
// is the emitter's namespace; ccpool itself knows nothing of the janitor.
const (
	gaugeJanitorLastSuccess = "ccpool_janitor_last_success_timestamp_seconds"
	gaugeJanitorLastRun     = "ccpool_janitor_last_run_seconds"
	gaugeJanitorDeadlineHit = "ccpool_janitor_deadline_hit"
	gaugeJanitorPassErrors  = "ccpool_janitor_pass_errors"
	gaugeJanitorReclaimed   = "ccpool_janitor_reclaimed"
	gaugeOrphanLastEvent    = "ccpool_orphan_last_event_timestamp_seconds"

	gaugeEmitTimeout = 10 * time.Second
)

// gaugeEmitFn emits one gauge point; nil disables emission (tests).
type gaugeEmitFn func(ctx context.Context, name string, value float64, labels map[string]string) error

// ccpoolGaugeEmit is the production gaugeEmitFn.
func ccpoolGaugeEmit(ctx context.Context, name string, value float64, labels map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, gaugeEmitTimeout)
	defer cancel()
	return ccpool.NewCLIRunner(config.Default()).EmitGauge(ctx, name, value, labels)
}

// allJanitorPasses is every pass name, so a pass that did not run still reports
// 0 rather than leaving a stale series.
var allJanitorPasses = []string{passClosedBead, passDefaultPool, passOrphan, passWorktree, passLockGC}

// emitJanitorGauges publishes the heartbeat of one run that was not skipped.
// Best effort: a failed emit is logged and never changes the run's outcome.
// last_success moves only on a completed run, so a run that keeps hitting its
// deadline goes stale as well as raising the deadline gauge.
func emitJanitorGauges(ctx context.Context, emit gaugeEmitFn, now time.Time, outcome string, elapsed time.Duration, stats map[string]*passStat) {
	if emit == nil {
		return
	}
	put := func(name string, v float64, labels map[string]string) {
		if err := emit(ctx, name, v, labels); err != nil {
			slog.Warn("janitor: gauge emit failed", "gauge", name, "err", err)
		}
	}
	if outcome == janitorCompleted {
		put(gaugeJanitorLastSuccess, float64(now.Unix()), nil)
	}
	put(gaugeJanitorLastRun, elapsed.Seconds(), nil)
	hit := 0.0
	if outcome == janitorDeadline {
		hit = 1
	}
	put(gaugeJanitorDeadlineHit, hit, nil)
	for _, p := range allJanitorPasses {
		var errs, rec float64
		if st := stats[p]; st != nil {
			errs, rec = float64(st.Errors), float64(st.Reclaimed)
		}
		put(gaugeJanitorPassErrors, errs, map[string]string{"pass": p})
		put(gaugeJanitorReclaimed, rec, map[string]string{"pass": p})
	}
}

// orphanGaugeEmit, when set (by main), publishes the time of the latest orphan
// event per role and kind. It stays nil under test.
var orphanGaugeEmit gaugeEmitFn

// emitOrphanGauge records an orphan_reclaimed / orphan_hard_stop event as a
// gauge, the only orphan-frequency signal besides events.jsonl.
func emitOrphanGauge(role, kind string, now time.Time) {
	if orphanGaugeEmit == nil {
		return
	}
	if err := orphanGaugeEmit(context.Background(), gaugeOrphanLastEvent, float64(now.Unix()), map[string]string{"role": role, "kind": kind}); err != nil {
		slog.Warn("orphan: gauge emit failed", "err", err)
	}
}
