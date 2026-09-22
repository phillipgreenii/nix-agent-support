package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
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
// Reuses closeSessionAndWorktree and beadAlreadyClosed (preshutdown.go)
// unmodified for the actual purge and the bead-status check respectively —
// the SAME fail-closed bd lookup teardownAllSessions's own shutdown-time
// sweep already uses. Returns the number of sessions actually closed.
//
// Deliberately narrower than closeUnlessNeedsInput's own shutdown-time
// decision: at shutdown EVERY session is torn down regardless of state (the
// whole daemon is exiting, so "still working" no longer means anything —
// see closeUnlessNeedsInput's own doc). Here the daemon keeps running, so a
// session actively working a turn, or in a transitional state this sweep
// does not recognize as "done with nothing left to do" (starting/ready/
// errored), is left untouched even once its bead has closed — only
// StateIdle/StateNeedsInput qualify (reconcilableState below), matching
// pg2-hrppg's own Ask verbatim: "if the bead is closed AND the session is
// idle/needs_input (not actively working), close/purge the session."
func reconcileClosedBeadSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix string) (closed int) {
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
		if closeSessionAndWorktree(ctx, cc, open, s) {
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
