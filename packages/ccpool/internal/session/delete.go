package session

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Run lifecycle metrics (pg2-om899.5). Every ended session RUN is emitted
// exactly once as one ccpool_sessions_closed_total increment plus one
// ccpool_session_duration_seconds observation (ended_at - started_at of the
// run). The invariants and the mechanism chosen for each:
//
//  1. Emitted at most once, and before its row is deleted. Mechanism: the
//     conditional claim Store.ClaimRunEmission (UPDATE ... SET metrics_emitted
//     = 1 WHERE id = ? AND metrics_emitted = 0 AND ended_at IS NOT NULL); a
//     run is recorded only by the caller whose claim won (never check-then-act).
//     deleteSessionLocked emits every ended unemitted run before Store.Delete.
//  2. Finalization is conditional and idempotent. Mechanism: runs are ended only
//     through endOpenRun -> Store.FinalizeRun (a single UPDATE ... WHERE
//     ended_at IS NULL); losing the race is a no-op, never an error.
//  3. Every delete/finalize decision is re-read under the per-external_id lock.
//     Mechanism: deleteSessionLocked requires the lock and re-reads the row and
//     its runs; reapDeadRow (Pass 0) takes the lock and re-reads the row before
//     deciding, skipping a row that was resumed.
//  4. A pending reason wins over the guess. Mechanism: FinalizeRun keeps a
//     non-empty end_reason over the supplied reason, so every non-hook
//     finalize passes `exited`/reaper/last_activity_at as the fallback only.
//
// The process that records is nil-safe: with no OTLP environment the Record*
// call is a no-op, yet the claim has still been won, so the flag is set once
// emission has been ATTEMPTED (manual CLI invocations without an OTLP endpoint
// are not counted, by design).

// emitRun claims one ended run and records its lifecycle metrics if the claim
// won. attrs MUST have been resolved before any delete of the session.
func (s *Service) emitRun(ctx context.Context, externalID string, run store.Run, attrs []attribute.KeyValue) error {
	won, err := s.d.Store.ClaimRunEmission(ctx, run.ID)
	if err != nil || !won {
		return err
	}
	dur := run.EndedAt - run.StartedAt
	if dur < 0 {
		dur = 0
	}
	result := run.EndReason
	if !store.CloseReasons[result] {
		result = "exited"
	}
	recordSessionClosed(float64(dur), result, attrs)
	s.attributeRunTokens(ctx, externalID, run, attrs)
	return nil
}

// attributeRunTokens attributes the output tokens one ended run produced to its
// pool (ccpool_session_output_tokens_total), so a finished session's tokens
// survive the disappearance of its live-session gauge series. The total is the
// transcript's distinct-message output_tokens; the run's own contribution is
// that total minus the largest snapshot of the session's earlier runs
// (Store.RecordRunOutputTokens), because a resume appends to the SAME
// transcript. Best-effort and never fatal: a nil Transcript, an empty
// transcript path, an unreadable transcript or a store error records nothing
// and logs, and because the snapshot only advances on success a later run
// catches the tokens up. A session whose transcript is already gone (a pruned
// phantom) cannot be attributed. Called only by the emitter that won the run's
// emission claim, so it runs at most once per run. attrs carries pool and the
// allowlisted labels and NO per-session id: the counter stays bounded by
// pools x roles.
func (s *Service) attributeRunTokens(ctx context.Context, externalID string, run store.Run, attrs []attribute.KeyValue) {
	if s.d.Transcript == nil {
		return
	}
	row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
	if err != nil || !ok || row.TranscriptPath == "" {
		return
	}
	total, err := s.d.Transcript.OutputTokens(row.TranscriptPath)
	if err != nil {
		slog.Warn("ccpool: read transcript output tokens failed", append([]any{"err", err}, sessionLogArgs(externalID)...)...)
		return
	}
	delta, err := s.d.Store.RecordRunOutputTokens(ctx, run.ID, total)
	if err != nil {
		slog.Warn("ccpool: record run output tokens failed", append([]any{"err", err}, sessionLogArgs(externalID)...)...)
		return
	}
	if delta > 0 {
		recordSessionOutputTokens(delta, attrs)
	}
}

// emitEndedRuns emits every ended, not-yet-emitted run of externalID, whoever
// ended it. Caller holds the per-external_id lock.
func (s *Service) emitEndedRuns(ctx context.Context, externalID string, attrs []attribute.KeyValue) error {
	runs, err := s.d.Store.RunsFor(ctx, externalID)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Open() || r.MetricsEmitted {
			continue
		}
		if err := s.emitRun(ctx, externalID, r, attrs); err != nil {
			return err
		}
	}
	return nil
}

// emitPendingRuns is the reaper sweep's start step: it emits every ended run
// that no in-process emitter saw (ended by the lock-free SessionEnd hook, or by
// Pass 0 on a kept row), BEFORE Pass 0 can delete a session row. Failures are
// logged, not fatal: the claim is conditional, so the next sweep retries.
func (s *Service) emitPendingRuns(ctx context.Context) {
	pending, err := s.d.Store.RunsPendingEmission(ctx)
	if err != nil {
		slog.Warn("ccpool: list runs pending metrics emission failed", "err", err)
		return
	}
	for _, p := range pending {
		if err := s.emitRun(ctx, p.ExternalID, p.Run, s.metricAttrs(p.ExternalID)); err != nil {
			slog.Warn("ccpool: emit run metrics failed", append([]any{"err", err}, sessionLogArgs(p.ExternalID)...)...)
		}
	}
}

// deleteSessionLocked is THE one place Store.Delete is called (enforced by
// TestNoStoreDeleteOutsideHelper). The caller MUST already hold the
// per-external_id lock (closeWithReason, the Create path and reapDeadRow all
// do; calling a locking form from inside them would self-deadlock). Before
// deleting it finalizes any still-open run (pending end_reason if written,
// else exited, end_source=reaper, ended_at = last_activity_at), emits every
// ended run with metrics_emitted = 0, and only then deletes (which removes the
// runs and the session's metadata, hence attrs are resolved first).
func (s *Service) deleteSessionLocked(ctx context.Context, externalID string) error {
	attrs := s.metricAttrs(externalID)
	if row, ok, err := s.d.Store.GetByExternalID(ctx, externalID); err != nil {
		return err
	} else if ok {
		if err := s.endOpenRun(ctx, externalID, "exited", store.RunEndReaper, row.LastActivityAt); err != nil {
			return fmt.Errorf("finalize run before delete: %w", err)
		}
		if err := s.emitEndedRuns(ctx, externalID, attrs); err != nil {
			return fmt.Errorf("emit run metrics before delete: %w", err)
		}
	}
	return s.d.Store.Delete(ctx, externalID)
}
