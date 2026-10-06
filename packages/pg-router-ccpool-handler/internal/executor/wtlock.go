package executor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
)

// Worktree liveness lock (bead pg2-ganjb, INV-CCH-19).
//
// A handler killed between isolation.Ensure and CC.Ensure leaks the per-bead
// worktree and its pg-router/<bead> anchor branch with no session row, so
// nothing keyed on a row can find it. The dispatch-time worktree sweep
// (cmd/pg-router-ccpool-handler/worktreesweep.go) finds it by the worktree
// instead -- and needs a way to tell a leaked worktree from one a sibling
// dispatch is using right now, which looks identical (no row yet, or a row in
// another role's pool). That signal is a flock: every dispatch holds the
// per-bead worktree lock SHARED from before it creates the worktree until run()
// returns, and the sweep may act only while holding it EXCLUSIVE. The kernel
// releases a flock when its holder dies, so a SIGKILLed handler stops "holding"
// its worktree at the instant it dies, with no timer to tune.

const (
	// worktreeLockWait bounds how long a dispatch waits for an in-flight sweep
	// of the same bead's worktree (the sweep holds the lock only for a few git
	// calls).
	worktreeLockWait = 10 * time.Second
	// worktreeLockStep is the retry step while waiting.
	worktreeLockStep = 50 * time.Millisecond
)

// holdWorktreeLock takes the shared per-bead worktree lock and returns its
// release function (idempotent; safe to defer). It is a no-op without a
// configured lock dir (unit tests; production always sets one). A failure to
// take the lock is logged and the dispatch proceeds unprotected: the sweep's age
// grace and its dirty/commit/session-row guards still apply, and a dispatch must
// never fail because a sweep, a lock file or a filesystem hiccup got in the way.
func (r *ccpoolRun) holdWorktreeLock(ctx context.Context, beadID string) (release func()) {
	if r.deps.LockDir == "" || beadID == "" {
		return func() {}
	}
	key := sessionlock.WorktreeKey(beadID)
	deadline := time.Now().Add(worktreeLockWait)
	for {
		l, err := sessionlock.TryRLock(r.deps.LockDir, key)
		if err == nil {
			return l.Unlock
		}
		if !errors.Is(err, sessionlock.ErrHeld) || time.Now().After(deadline) {
			slog.Warn("dispatch: could not take the worktree lock; proceeding without it",
				"bead", beadID, "err", err)
			return func() {}
		}
		select {
		case <-ctx.Done():
			return func() {}
		case <-time.After(worktreeLockStep):
		}
	}
}
