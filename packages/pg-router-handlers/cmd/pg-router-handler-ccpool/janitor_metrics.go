package main

import (
	"context"
	"log/slog"
	"sync"
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
	// gaugeJanitorRoleSuccess is the per-role heartbeat: set for every role that
	// finished all its passes inside its own share, even in a run whose outcome is
	// deadline because another role was cut. The stale alert reads the newest of
	// this and last_success.
	gaugeJanitorRoleSuccess = "ccpool_janitor_role_last_success_timestamp_seconds"
	gaugeJanitorDeadlineHit = "ccpool_janitor_deadline_hit"
	gaugeJanitorPassErrors  = "ccpool_janitor_pass_errors"
	gaugeJanitorReclaimed   = "ccpool_janitor_reclaimed"
	gaugeOrphanLastEvent    = "ccpool_orphan_last_event_timestamp_seconds"

	// Progress gauges (design D2), per pass and role for the counters.
	gaugeJanitorCandidates = "ccpool_janitor_candidates"
	gaugeJanitorExamined   = "ccpool_janitor_examined"
	gaugeJanitorCached     = "ccpool_janitor_cached"
	gaugeJanitorKept       = "ccpool_janitor_kept"
	gaugeJanitorUnexamined = "ccpool_janitor_unexamined"
	gaugeJanitorPhase      = "ccpool_janitor_phase_seconds"
	gaugeJanitorOldestAge  = "ccpool_janitor_oldest_unexamined_age_seconds"

	gaugeEmitTimeout  = 10 * time.Second
	gaugeBatchTimeout = 30 * time.Second
)

// gaugeEmitFn emits one gauge point; nil disables emission (tests).
type gaugeEmitFn func(ctx context.Context, name string, value float64, labels map[string]string) error

// ccpoolGaugeEmit is the production gaugeEmitFn.
func ccpoolGaugeEmit(ctx context.Context, name string, value float64, labels map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, gaugeEmitTimeout)
	defer cancel()
	return ccpool.NewCLIRunner(config.Default()).EmitGauge(ctx, name, value, labels)
}

// gaugeBatch collects a run's gauges and publishes them with ONE
// `ccpool emit-gauge` call. Emitting each point through its own subprocess made
// a run spawn dozens of ccpool processes, each dialing the collector and
// flushing on exit; under load every one outlived its timeout and was killed,
// so no gauge ever arrived.
type gaugeBatch struct {
	mu     sync.Mutex
	points []ccpool.GaugePoint
	send   func(ctx context.Context, points []ccpool.GaugePoint) error
}

func newGaugeBatch() *gaugeBatch {
	return &gaugeBatch{send: func(ctx context.Context, pts []ccpool.GaugePoint) error {
		return ccpool.NewCLIRunner(config.Default()).EmitGauges(ctx, pts)
	}}
}

// Emit is a gaugeEmitFn: it only buffers, so it never blocks or fails.
func (b *gaugeBatch) Emit(_ context.Context, name string, value float64, labels map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.points = append(b.points, ccpool.GaugePoint{Name: name, Value: value, Labels: labels})
	return nil
}

// Flush sends and clears the buffered points under one bounded call. It runs on
// a context detached from ctx's cancellation so a run that ended at its
// deadline still publishes the heartbeat that says so.
func (b *gaugeBatch) Flush(ctx context.Context) error {
	b.mu.Lock()
	pts := b.points
	b.points = nil
	b.mu.Unlock()
	if len(pts) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gaugeBatchTimeout)
	defer cancel()
	return b.send(ctx, pts)
}

// flushGauges publishes the buffered gauges, logging (never returning) a failure.
func flushGauges(ctx context.Context, flush func(context.Context) error) {
	if flush == nil {
		return
	}
	if err := flush(ctx); err != nil {
		slog.Warn("janitor: gauge batch emit failed", "err", err)
	}
}

// allJanitorPasses is every pass name, so a pass that did not run still reports
// 0 rather than leaving a stale series.
var allJanitorPasses = []string{passClosedBead, passDefaultPool, passOrphan, passWorktree, passLockGC}

// emitJanitorGauges publishes the heartbeat of one run that was not skipped.
// Best effort: a failed emit is logged and never changes the run's outcome.
// last_success moves only on a completed run, so a run that keeps hitting its
// deadline goes stale as well as raising the deadline gauge.
//
// rolesDone are the roles that finished every pass inside their own share; each
// gets a per-role success point, so a run cut at one role's share (outcome
// deadline, unchanged) still shows the other roles were swept.
func emitJanitorGauges(ctx context.Context, emit gaugeEmitFn, now time.Time, outcome string, elapsed time.Duration, stats map[string]*passStat, rolesDone []string) {
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
	for _, r := range rolesDone {
		put(gaugeJanitorRoleSuccess, float64(now.Unix()), map[string]string{"role": r})
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

// emitJanitorProgressGauges publishes the progress counters of one run that was
// not skipped (design D2). Best effort, like the heartbeat. The oldest
// unexamined age is per pass, the largest over the roles that ran it, and is
// reported only for a pass that ran: a run that never reached the pass must not
// reset the age to zero and hide a stagnation.
func emitJanitorProgressGauges(ctx context.Context, emit gaugeEmitFn, counts []*passCounts) {
	if emit == nil {
		return
	}
	put := func(name string, v float64, labels map[string]string) {
		if err := emit(ctx, name, v, labels); err != nil {
			slog.Warn("janitor: gauge emit failed", "gauge", name, "err", err)
		}
	}
	oldest := map[string]float64{}
	ran := map[string]bool{}
	for _, c := range counts {
		ran[c.Pass] = true
		l := map[string]string{"pass": c.Pass, "role": c.Role}
		put(gaugeJanitorCandidates, float64(c.Candidates), l)
		put(gaugeJanitorExamined, float64(c.Examined), l)
		put(gaugeJanitorCached, float64(c.Cached), l)
		put(gaugeJanitorKept, float64(c.Kept), l)
		put(gaugeJanitorUnexamined, float64(c.Unexamined), l)
		for phase, ms := range map[string]*int64{phaseList: c.ListMS, phaseProbe: c.ProbeMS, phaseScan: c.ScanMS} {
			if ms != nil {
				put(gaugeJanitorPhase, float64(*ms)/1000, map[string]string{"pass": c.Pass, "role": c.Role, "phase": phase})
			}
		}
		if c.OldestUnexaminedAgeSeconds > oldest[c.Pass] {
			oldest[c.Pass] = c.OldestUnexaminedAgeSeconds
		}
	}
	for _, p := range allJanitorPasses {
		if ran[p] {
			put(gaugeJanitorOldestAge, oldest[p], map[string]string{"pass": p})
		}
	}
}

// orphanGaugeEmit, when set (by main), publishes the time of the latest orphan
// event per role and kind. It stays nil under test.
var orphanGaugeEmit gaugeEmitFn

// emitOrphanGauge records an orphan_reclaimed / orphan_hard_stop event as a
// gauge, the only orphan-frequency signal besides events.jsonl.
func emitOrphanGauge(ctx context.Context, role, kind string, now time.Time) {
	if orphanGaugeEmit == nil {
		return
	}
	if err := orphanGaugeEmit(ctx, gaugeOrphanLastEvent, float64(now.Unix()), map[string]string{"role": role, "kind": kind}); err != nil {
		slog.Warn("orphan: gauge emit failed", "err", err)
	}
}
