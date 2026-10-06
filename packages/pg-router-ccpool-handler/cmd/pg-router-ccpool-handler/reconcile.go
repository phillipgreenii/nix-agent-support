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
// Scope: it runs against ONE pool -- the runner it is handed. From
// dispatch.go's runDispatch that is the dispatching role's own pool
// (deps.CC, scoped by buildDeps), so a role reconciles only its own pool's
// sessions; query.go's call uses the default-pool runner. It handles closed
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
// query.go's own call is kept as defense-in-depth for a future deployment
// that DOES configure a [[query]] source here, but dispatch.go's call is the
// one that actually fires today.
//
// postStartup/preShutdown each fire exactly once (args.go's usageLine doc),
// so neither is a candidate for a periodic-ish sweep either — dispatch is
// the only remaining invocation, and it recurs as often as new work arrives
// for this handler's roles, which is how the original zr-50s7h.2 incident
// (bursty review/feedback dispatch) surfaced in the first place.
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
func reconcileClosedBeadSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot string, quiet quietCheck) (closed int) {
	sessions, err := cc.List(ctx)
	if err != nil {
		slog.Warn("reconcile: list failed", "err", err)
		return 0
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, prefix) {
			continue
		}
		if !reconcilableState(s.State) {
			continue
		}
		if !beadAlreadyClosed(ctx, br, s) {
			continue
		}
		if quiet != nil && !quiet(s) {
			slog.Info("reconcile: session transcript still active; deferring", "session", s.ExternalID)
			continue
		}
		if closeSession(ctx, cc, open, repoRoot, s, worktreeInUseByPeer(sessions, s)) {
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
