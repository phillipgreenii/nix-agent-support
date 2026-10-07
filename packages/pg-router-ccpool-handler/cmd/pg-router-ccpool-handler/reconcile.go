package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
)

// reconcileClosedBeadSessions closes every prefix-matching session whose own
// bead has ALREADY closed and which is StateIdle or StateNeedsInput — never a
// session that is StateWorking (or any other state), and never a session
// whose bead is still open (or whose bead status could not be determined;
// beadAlreadyClosed fails closed) — pg2-hrppg's fix for the gap
// teardownAllSessions/closeUnlessNeedsInput (preshutdown.go) deliberately
// left open: that sweep only ever runs ONCE, at daemon shutdown
// (args.go/run.go's own "once-per-process-lifetime" framing), so a
// needs_input or idle session whose bead closed while the daemon kept
// running sat there burning a live tmux/claude process until the NEXT
// shutdown (bead zr-50s7h.2: closed 2026-09-19, its own session still live
// ~2.5 days later).
//
// Widening (pg2-wqi3e): dispatch.go also runs it against the DEFAULT pool, which
// no role's pool covers and ccpool's own reaper never closes (a not-live row with
// a resumable transcript is kept, and needs_input is preserved for a human). That
// pass is guarded (reconcileStaleDefaultPoolSessions, stalesession.go): idle for
// staleSessionMinIdle and a worktree that is absent or clean and pushed.
//
// Scope: it runs against ONE pool -- the runner it is handed. From
// dispatch.go's runDispatch that is the dispatching role's own pool
// (deps.CC, scoped by buildDeps), so a role reconciles only its own pool's
// sessions; query.go's call is the guarded default-pool pass. It handles closed
// beads only; sessions whose HANDLER died while the bead is still open are the
// separate, role-scoped orphan reconcile (orphan.go, INV-CCH-18), which
// runDispatch runs right after this one.
//
// Invoked primarily from dispatch.go's runDispatch, once per queued item for
// every enabled ccpool role (review/feedback/worker) — the invocation
// actually GUARANTEED to recur frequently in the live deployment. An earlier
// revision of this fix hooked query.go's runQuery instead (that subcommand's
// own periodic PeriodTrigger pull, packages/pg-router/internal/query/
// trigger.go, looked like the natural existing polling mechanism to reuse),
// but production verification (the coordinator checking the live daemon
// config and phillipg-nix-ziprecruiter's modules/zm/default.nix directly)
// found this handler is never wired as a [[query]] source there at all —
// PG_ROUTER_HANDLER_COMMAND only ever backs the `dispatch` subcommand for
// review/feedback/worker, and nothing sets --query-config/
// PG_ROUTER_CCPOOL_HANDLER_QUERY — so that hook was dead code as deployed.
// query.go's own call (since pg2-wqi3e the guarded default-pool pass) is kept
// as defense-in-depth for a future deployment that DOES configure a [[query]]
// source here, but dispatch.go's call is the one that actually fires today.
//
// postStartup/preShutdown each fire exactly once (args.go's usageLine doc),
// so neither is a candidate for a periodic-ish sweep either — dispatch is
// the only remaining invocation, and it recurs as often as new work arrives
// for this handler's roles, which is how the original zr-50s7h.2 incident
// (bursty review/feedback dispatch) surfaced in the first place.
//
// Two-phase teardown (bead pg2-kqegi, INV-CCH-20): a closed-bead session in a
// per-bead linked worktree is purged only AFTER its worktree is removed
// (closeSession), and this pass first retries one row whose earlier teardown was
// interrupted (retryPurgePending, purgepending.go). worktreeDir is the handler's
// cfg.WorktreeDir; empty keeps every teardown single-phase.
//
// Reuses closeSessionAndWorktree and beadAlreadyClosed (preshutdown.go) for
// the actual purge (worktree removal AND its own pg-router/<beadID> anchor
// branch delete, bead pg2-ci75j via pg2-tpa18) and the bead-status check
// respectively — the SAME fail-closed bd lookup teardownAllSessions's own
// shutdown-time sweep already uses. Returns the number of sessions actually
// closed.
//
// Deliberately different from closeUnlessNeedsInput's own shutdown-time
// decision: that sweep also spares actively working sessions (pg2-hwt7v) but
// purges errored sessions and any idle/needs_input one at shutdown. Here the
// daemon keeps running, so a session actively working a turn, or in a
// transitional state this sweep does not recognize as "done with nothing
// left to do" (starting/ready/errored), is left untouched even once its bead has closed — only
// StateIdle/StateNeedsInput qualify (reconcilableState below), matching
// pg2-hrppg's own Ask verbatim: "if the bead is closed AND the session is
// idle/needs_input (not actively working), close/purge the session."
//
// quiet (pg2-03icc) is the transcript-quiet guard: a session whose
// transcript or subagent transcripts were written within the configured
// window is skipped this sweep (idle-with-running-subagents, pg2-9fwft) and
// retried by the next one. nil disables the guard.
func reconcileClosedBeadSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot, worktreeDir string, quiet quietCheck) (closed int) {
	return reconcileClosedBeadSessionsGuarded(ctx, cc, open, br, prefix, repoRoot, worktreeDir, quiet, nil, "reconcile: list failed")
}

// sessionGuard is an extra, caller-supplied precondition on closing a
// closed-bead session (pg2-wqi3e): it reports whether s may be closed now, and
// otherwise why not (logged). It runs only after the bead is confirmed closed,
// so its (git) work is never spent on a session whose bead is still open. nil
// means no extra condition.
type sessionGuard func(ctx context.Context, s ccpool.Session) (ok bool, reason string)

// reconcileClosedBeadSessionsGuarded is reconcileClosedBeadSessions with an
// optional guard (see sessionGuard) and the log line used when the pool cannot
// be listed. reconcileStaleDefaultPoolSessions (stalesession.go) uses it to
// widen the sweep to the default pool safely.
func reconcileClosedBeadSessionsGuarded(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot, worktreeDir string, quiet quietCheck, guard sessionGuard, listFailMsg string) (closed int) {
	sessions, err := cc.List(ctx)
	if err != nil {
		slog.Warn(listFailMsg, "err", err)
		return 0
	}
	// Purge_pending retry (bead pg2-kqegi, INV-CCH-20), FIRST and from the same
	// snapshot: finish at most ONE unfinished two-phase teardown per pass (the
	// removal of a 100k-file checkout can take minutes, and this runs in the
	// dispatch path). A row skipped by a guard (mid-turn, transcript active) is not
	// an attempt, so the next marked row gets the slot.
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, prefix) || !s.PurgePending() {
			continue
		}
		purged, attempted := retryPurgePending(ctx, cc, open, repoRoot, worktreeDir, quiet, sessions, s)
		if purged {
			closed++
		}
		if attempted {
			break
		}
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, prefix) {
			continue
		}
		// EXCLUSIVITY (INV-CCH-20): a marked row was handled above (or is left to a
		// later pass); this branch must never touch it, or one row would be closed
		// twice in a single pass.
		if s.PurgePending() {
			continue
		}
		if !reconcilableState(s.State) {
			continue
		}
		if !beadAlreadyClosed(ctx, br, s) {
			continue
		}
		if guard != nil {
			if ok, reason := guard(ctx, s); !ok {
				slog.Info("reconcile: preserving closed-bead session", "session", s.ExternalID, "reason", reason)
				continue
			}
		}
		if quiet != nil && !quiet(s) {
			slog.Info("reconcile: session transcript still active; deferring", "session", s.ExternalID)
			continue
		}
		if closeSession(ctx, cc, open, repoRoot, worktreeDir, s, worktreeInUseByPeer(sessions, s)) {
			closed++
		}
	}
	return closed
}

// reconcilableState reports whether state is one reconcileClosedBeadSessions
// may purge once its session's bead has closed — StateIdle or
// StateNeedsInput only (pg2-hrppg's Ask). StateWorking is never reconciled
// regardless of bead status (a live in-flight dispatch, not an orphan); nor
// is StateStarting/StateReady/StateErrored — this sweep runs continuously
// while the daemon is up, unlike the once-per-shutdown teardownAllSessions,
// so it must never race a session that might still be doing something.
func reconcilableState(s ccpool.SessionState) bool {
	return s == ccpool.StateIdle || s == ccpool.StateNeedsInput
}

// worktreeInUseByPeer reports whether another session in sessions is still
// actively using s's working directory (pg2-u3t04 / pg2-aqpqx). A per-bead
// worktree path is keyed by bead id alone (worktree.Ensure), so every role's
// session for one bead (review, feedback, ...) shares it: purging one idle
// closed-bead session must not delete the directory out from under a peer
// that is starting, ready or working. A peer that is itself idle/needs_input
// is not protected here: the same sweep reclaims it on the same terms.
func worktreeInUseByPeer(sessions []ccpool.Session, s ccpool.Session) bool {
	if s.CWD == "" {
		return false
	}
	for _, o := range sessions {
		if o.ExternalID == s.ExternalID || o.CWD != s.CWD || !o.Live {
			continue
		}
		if !reconcilableState(o.State) && o.State != ccpool.StateErrored {
			return true
		}
	}
	return false
}

// quietCheck reports whether s has had no transcript activity recently
// enough that its worktree is safe to reclaim.
type quietCheck func(s ccpool.Session) bool

// newTranscriptQuietCheck builds a quietCheck from the same mtime scan the
// executor's waitSessionQuiet uses (session transcript plus sibling
// subagents/*.jsonl). Unlike waitSessionQuiet it never blocks: a still-active
// session is simply deferred to the next sweep. window <= 0, no transcript
// path, or an unreadable transcript all report quiet (nothing observable, the
// same fail direction as waitSessionQuiet). latest and now are injectable;
// nil selects executor.LatestTranscriptActivity and time.Now.
func newTranscriptQuietCheck(window time.Duration, latest func(string) (time.Time, bool), now func() time.Time) quietCheck {
	if window <= 0 {
		return nil
	}
	if latest == nil {
		latest = executor.LatestTranscriptActivity
	}
	if now == nil {
		now = time.Now
	}
	return func(s ccpool.Session) bool {
		if s.TranscriptPath == "" {
			return true
		}
		t, ok := latest(s.TranscriptPath)
		return !ok || now().Sub(t) >= window
	}
}
