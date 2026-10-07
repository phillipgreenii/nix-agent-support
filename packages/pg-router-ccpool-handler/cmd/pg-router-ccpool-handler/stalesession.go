package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
)

// Stale default-pool session reconcile (bead pg2-wqi3e).
//
// A not-live needs_input row in ccpool's DEFAULT pool whose bead has closed is
// reachable by no other reclaim: ccpool's reaper keeps a not-live row with a
// resumable transcript (Pass 0) and spares needs_input (preservedForHuman); the
// closed-bead reconcile only ever ran against the dispatching role's own pool;
// the orphan reconcile needs a supervision lease the row predates and leaves
// needs_input alone. The operator's requirement (2026-10-07): "a non-live idle
// session has to be cleaned up in some fashion."
//
// reconcileStaleDefaultPoolSessions closes such rows by running the closed-bead
// reconcile against the default pool WITH a guard (staleSessionGuard), because
// the teardown it reaches (closeSession) force-removes the session's worktree
// with no dirty/unpushed check of its own. A session is closed only when ALL hold:
//
//   - its bead is closed (beadAlreadyClosed: a missing pgrouter.bead tag, a bd
//     error and a bead bd cannot find all PRESERVE -- fail closed);
//   - it is idle/needs_input (reconcilableState), never working;
//   - it has been idle for at least staleSessionMinIdle: the newer of the row's
//     last_activity_at (ccpool list --json) and its transcript's newest mtime;
//     with neither known the age is unknown and the row is preserved;
//   - its working directory is absent, OR is a git worktree that is clean and
//     holds nothing beyond its upstream (or, with no upstream, beyond the
//     canonical clone's HEAD) -- an unreadable status/branch/count preserves.
//
// The teardown is non-purge-safe for the transcript: closeSession purges the
// ccpool ROW only, never the ~/.claude transcript.

// staleSessionMinIdle is how long a closed-bead session must have been idle
// before the default-pool reconcile may close it. It is a suggestion from the
// bead (7 days), not a measured value: long enough that no operator who still
// intends to `ccpool attach` has plausibly gone quiet this long, short enough
// that rows do not accumulate for weeks.
const staleSessionMinIdle = 7 * 24 * time.Hour

// reconcileStaleDefaultPoolSessions runs the closed-bead reconcile against cc
// (the default pool) with guard applied; it returns how many sessions it closed.
func reconcileStaleDefaultPoolSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot, worktreeDir string, quiet quietCheck, guard sessionGuard) int {
	return reconcileClosedBeadSessionsGuarded(ctx, cc, open, br, prefix, repoRoot, worktreeDir, quiet, guard, "reconcile: list failed (default pool)")
}

// reconcileDefaultPool is the production wiring of reconcileStaleDefaultPoolSessions
// (dispatch.go, query.go): ccpool's shared default pool (reached whatever
// CCPOOL_POOL this process inherited), the bd tracker at cfg.RepoRoot, the
// real git opener, and the real clock and transcript scan. A session whose
// bead lives in another tracker fails the bd lookup and is preserved.
func reconcileDefaultPool(ctx context.Context, cfg config.Config) int {
	guard := newStaleSessionGuard(gitWorktreeOpener, cfg.RepoRoot, staleSessionMinIdle, nil, nil)
	return reconcileStaleDefaultPoolSessions(ctx, ccpool.NewCLIRunnerDefaultPool(cfg), gitWorktreeOpener,
		beads.NewCLIRunnerForRepo(cfg.RepoRoot, ""), cfg.SessionPrefix, cfg.RepoRoot, cfg.WorktreeDir,
		newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil), guard)
}

// newStaleSessionGuard builds the guard for the default-pool pass. now and
// latest are injectable; nil selects time.Now and executor.LatestTranscriptActivity.
func newStaleSessionGuard(open worktree.Opener, repoRoot string, minIdle time.Duration, now func() time.Time, latest func(string) (time.Time, bool)) sessionGuard {
	if now == nil {
		now = time.Now
	}
	if latest == nil {
		latest = executor.LatestTranscriptActivity
	}
	return func(ctx context.Context, s ccpool.Session) (bool, string) {
		age, known := sessionIdleAge(s, now(), latest)
		if !known {
			return false, "idle age unknown (no last_activity_at and no readable transcript)"
		}
		if age < minIdle {
			return false, fmt.Sprintf("idle %s, under the %s threshold", age.Round(time.Second), minIdle)
		}
		return worktreeSafeToClose(ctx, open, repoRoot, s.CWD)
	}
}

// sessionIdleAge is how long s has been idle: now minus the NEWER of its
// last_activity_at and its transcript's newest mtime. known is false when
// neither is available.
func sessionIdleAge(s ccpool.Session, now time.Time, latest func(string) (time.Time, bool)) (age time.Duration, known bool) {
	var last time.Time
	if s.LastActivityAt > 0 {
		last, known = time.Unix(s.LastActivityAt, 0), true
	}
	if s.TranscriptPath != "" {
		if t, ok := latest(s.TranscriptPath); ok && (!known || t.After(last)) {
			last, known = t, true
		}
	}
	if !known {
		return 0, false
	}
	return now.Sub(last), true
}

// worktreeSafeToClose reports whether tearing down a session working in dir
// would lose nothing: dir is empty or absent, is not inside a git repository
// (nothing git could remove), is the canonical clone itself (git refuses to
// remove a main working tree), or is a worktree that is clean and has no commit
// beyond its upstream -- or, with no upstream, beyond the canonical clone's HEAD
// (the same notion of "work not lost" as the worktree-keyed sweep,
// worktreesweep.go). Anything it cannot read preserves.
func worktreeSafeToClose(ctx context.Context, open worktree.Opener, repoRoot, dir string) (bool, string) {
	if dir == "" {
		return true, ""
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return true, ""
	} else if err != nil {
		return false, "working directory unreadable: " + err.Error()
	}
	if samePath(dir, repoRoot) {
		return true, ""
	}
	wm, err := open(ctx, dir)
	if errors.Is(err, gitclient.ErrNotARepository) {
		return true, ""
	}
	if err != nil {
		return false, "cannot open the worktree: " + err.Error()
	}
	st, okSt := wm.(gitclient.StatusReader)
	refs, okRef := wm.(gitclient.RefReader)
	loc, okLoc := wm.(gitclient.Locator)
	if !okSt || !okRef || !okLoc {
		return false, "git client cannot read status/refs/branch"
	}
	dirty, err := st.Status(ctx)
	if err != nil {
		return false, "worktree status unreadable: " + err.Error()
	}
	if len(dirty) > 0 {
		return false, fmt.Sprintf("worktree has %d uncommitted entries", len(dirty))
	}
	hasUp, err := refs.HasUpstream(ctx)
	if err != nil {
		return false, "upstream unreadable: " + err.Error()
	}
	if hasUp {
		ahead, err := refs.CommitsAhead(ctx, "@{u}", "HEAD")
		if err != nil {
			return false, "unpushed-commit count unreadable: " + err.Error()
		}
		if ahead > 0 {
			return false, fmt.Sprintf("worktree has %d commits not on its upstream", ahead)
		}
		return true, ""
	}
	branch, err := loc.CurrentBranch(ctx)
	if err != nil {
		return false, "current branch unreadable (detached?): " + err.Error()
	}
	root, err := open(ctx, repoRoot)
	if err != nil {
		return false, "cannot open the canonical clone: " + err.Error()
	}
	rootRefs, ok := root.(gitclient.RefReader)
	if !ok {
		return false, "git client cannot count commits"
	}
	ahead, err := rootRefs.CommitsAhead(ctx, "HEAD", branch)
	if err != nil {
		slog.Debug("stale session guard: commit count unreadable", "branch", branch, "err", err)
		return false, "commit count unreadable: " + err.Error()
	}
	if ahead > 0 {
		return false, fmt.Sprintf("branch %s has %d commits the canonical clone's HEAD lacks (no upstream)", branch, ahead)
	}
	return true, ""
}
