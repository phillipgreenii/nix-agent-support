package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
)

// Two-phase teardown (bead pg2-kqegi, INV-CCH-20).
//
// closeSession (preshutdown.go) used to purge the ccpool row first and remove
// the worktree second. An interruption between the two (a daemon-restart drain
// timeout, a crash, a slow `git worktree remove --force` of a 1-2 GB checkout)
// left a half-removed worktree that no row led to, so no row-driven sweep could
// find it again (the original cause of pg2-me0t1's leak). For a per-bead linked
// worktree the order is now
//
//	SetMeta(purge_pending) -> Close(non-purge) -> RemoveWorktree -> branch delete -> Close(purge)
//
// and an interrupted or failed removal leaves the row, carrying the marker, for
// retryPurgePending to finish at the next same-pool dispatch reconcile (or, for
// the default pool, the next shutdown sweep).
//
// A marked row is owned by this retry alone: reconcileClosedBeadSessions and the
// orphan reconcile skip it, and crashOrphaned (internal/executor) treats it as
// absent so a redelivered dispatch never absorbs it.

// purgeMarkerValue is the value written under ccpool.MetaKeyPurgePending.
const purgeMarkerValue = "1"

// twoPhaseEligible reports whether s's teardown uses the two-phase order: its
// working directory is a per-bead linked worktree, i.e. it is not the repo root
// and lies strictly under worktreeDir (where worktree.Ensure creates them). An
// empty worktreeDir, or a "none"/"path"/"workforest" CWD, is not eligible and
// keeps the single-phase purge.
func twoPhaseEligible(s ccpool.Session, repoRoot, worktreeDir string) bool {
	if s.CWD == "" || worktreeDir == "" {
		return false
	}
	if samePath(s.CWD, repoRoot) {
		return false
	}
	return pathStrictlyUnder(s.CWD, worktreeDir)
}

// resolvePath returns p cleaned, with symlinks resolved when p exists.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// samePath reports whether a and b name the same directory, lexically or after
// symlink resolution.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b) || resolvePath(a) == resolvePath(b)
}

// pathStrictlyUnder reports whether path lies inside dir (never equal to it),
// lexically or after symlink resolution.
func pathStrictlyUnder(path, dir string) bool {
	under := func(p, d string) bool {
		rel, err := filepath.Rel(d, p)
		if err != nil || rel == "." || rel == ".." {
			return false
		}
		return len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator)
	}
	return under(filepath.Clean(path), filepath.Clean(dir)) || under(resolvePath(path), resolvePath(dir))
}

// finishPurge completes a two-phase teardown whose row is already closed
// (non-purge): remove the worktree (unless keepWorktree) and its anchor branch,
// and only then purge the row. It returns true iff the row was purged; on any
// failure the row is left, still carrying purge_pending, for the next retry.
func finishPurge(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session, keepWorktree bool) bool {
	if keepWorktree {
		slog.Info("teardown: worktree kept -- another live session still uses it",
			"session", s.ExternalID, "cwd", s.CWD)
	} else if !removeWorktreeForPurge(ctx, open, repoRoot, worktreeDir, s) {
		return false
	}
	if err := cc.Close(ctx, s.ExternalID, true); err != nil {
		slog.Warn("teardown: purge failed; the row keeps purge_pending for the next reconcile",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
		return false
	}
	return true
}

// removeWorktreeForPurge removes s's per-bead worktree from the REPO ROOT and
// then its anchor branch, and reports whether the worktree is now removed or
// confirmed gone (the only condition under which the row may be purged). A
// directory that no longer exists, or an unregistered husk under worktreeDir
// (no .git, or a .git git no longer knows), counts as gone; the husk is deleted
// and the registrations pruned.
func removeWorktreeForPurge(ctx context.Context, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session) bool {
	if err := ctx.Err(); err != nil {
		slog.Warn("teardown: interrupted before worktree removal; purge stays deferred",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
		return false
	}
	if _, err := os.Lstat(s.CWD); errors.Is(err, fs.ErrNotExist) {
		slog.Info("teardown: worktree already gone", "session", s.ExternalID, "cwd", s.CWD)
		pruneAndDeleteBranch(ctx, open, repoRoot, s)
		return true
	}
	root, err := open(ctx, repoRoot)
	if err != nil {
		slog.Warn("teardown: worktree remove failed (cannot open the repo root); purge stays deferred",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
		return false
	}
	rmErr := root.RemoveWorktree(ctx, s.CWD, true)
	if rmErr == nil {
		slog.Info("teardown: worktree removed", "session", s.ExternalID, "cwd", s.CWD)
		deleteAnchorBranch(ctx, open, repoRoot, s.ExternalID, s.Meta[ccpool.MetaKeyBead])
		return true
	}
	if _, err := os.Lstat(s.CWD); errors.Is(err, fs.ErrNotExist) {
		slog.Info("teardown: worktree gone after a failed removal", "session", s.ExternalID, "cwd", s.CWD, "err", rmErr)
		pruneAndDeleteBranch(ctx, open, repoRoot, s)
		return true
	}
	if unregisteredWorktreeDir(s.CWD, worktreeDir) {
		if err := os.RemoveAll(s.CWD); err != nil {
			slog.Warn("teardown: could not delete the unregistered worktree directory; purge stays deferred",
				"session", s.ExternalID, "cwd", s.CWD, "err", err)
			return false
		}
		slog.Info("teardown: unregistered worktree directory deleted", "session", s.ExternalID, "cwd", s.CWD, "err", rmErr)
		pruneAndDeleteBranch(ctx, open, repoRoot, s)
		return true
	}
	slog.Warn("teardown: worktree remove failed; purge stays deferred",
		"session", s.ExternalID, "cwd", s.CWD, "err", rmErr)
	return false
}

// pruneAndDeleteBranch prunes stale worktree registrations (best effort), so a
// prunable registration no longer pins the anchor branch, then deletes it.
func pruneAndDeleteBranch(ctx context.Context, open worktree.Opener, repoRoot string, s ccpool.Session) {
	if root, err := open(ctx, repoRoot); err != nil {
		slog.Warn("teardown: prune: open repo root failed", "session", s.ExternalID, "err", err)
	} else if err := root.PruneWorktrees(ctx); err != nil {
		slog.Warn("teardown: prune failed", "session", s.ExternalID, "err", err)
	}
	deleteAnchorBranch(ctx, open, repoRoot, s.ExternalID, s.Meta[ccpool.MetaKeyBead])
}

// unregisteredWorktreeDir reports whether path is a directory strictly under
// worktreeDir that git does not know as a linked worktree: it has no .git, or
// its .git file's mutual registration is gone (worktreeAdminDir). A .git
// DIRECTORY (a standalone clone) is never treated as removable.
func unregisteredWorktreeDir(path, worktreeDir string) bool {
	if worktreeDir == "" || !pathStrictlyUnder(path, worktreeDir) {
		return false
	}
	fi, err := os.Lstat(filepath.Join(path, ".git"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true
	case err != nil || fi.IsDir():
		return false
	}
	_, registered := worktreeAdminDir(path)
	return !registered
}

// worktreeHeldByLivePeer reports whether any OTHER session in sessions that is
// Live, in ANY state (idle and needs_input included), uses s's working
// directory. A redispatch for the same bead reuses the per-bead path
// (worktree.Ensure), so even an idle live peer keeps the worktree (INV-CCH-15).
func worktreeHeldByLivePeer(sessions []ccpool.Session, s ccpool.Session) bool {
	if s.CWD == "" {
		return false
	}
	for _, o := range sessions {
		if o.ExternalID != s.ExternalID && o.CWD == s.CWD && o.Live {
			return true
		}
	}
	return false
}

// retryPurgePending finishes an unfinished two-phase teardown of s (a row
// carrying purge_pending), from the same cc.List snapshot the caller holds. It
// returns purged (the row was purged) and attempted (a retry was actually made,
// as opposed to the row being skipped by a guard); a caller that bounds retries
// per pass counts only attempted ones.
//
// Guards, in order:
//   - a row that is still open and starting/ready/working is never touched: it
//     is mid-turn (its non-purge close failed), so it is retried once it settles;
//   - a still-open row whose transcript is not quiet is deferred (quiet may be
//     nil), like reconcileClosedBeadSessions;
//   - a still-open row is closed (non-purge) again first;
//   - ANY other Live session on the same CWD keeps the worktree: the row is
//     purged without removing anything (INV-CCH-15).
func retryPurgePending(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, quiet quietCheck, sessions []ccpool.Session, s ccpool.Session) (purged, attempted bool) {
	stillOpen := s.CloseReason == "" || s.Live
	if stillOpen {
		switch s.State {
		case ccpool.StateStarting, ccpool.StateReady, ccpool.StateWorking:
			slog.Info("reconcile: purge_pending row is mid-turn; leaving it for a later pass",
				"session", s.ExternalID, "state", string(s.State))
			return false, false
		}
		if quiet != nil && !quiet(s) {
			slog.Info("reconcile: purge_pending session transcript still active; deferring", "session", s.ExternalID)
			return false, false
		}
	}

	start := time.Now()
	attempt := s.PurgeAttempts() + 1
	slog.Info("reconcile: retrying purge_pending teardown",
		"session", s.ExternalID, "cwd", s.CWD, "attempt", attempt)
	if err := cc.SetMeta(ctx, s.ExternalID, ccpool.MetaKeyPurgeAttempts, strconv.Itoa(attempt)); err != nil {
		slog.Warn("reconcile: could not record the purge_pending attempt count", "session", s.ExternalID, "err", err)
	}

	fail := func(why string, err error) (bool, bool) {
		slog.Warn("reconcile: purge_pending retry failed; the row keeps its marker",
			"session", s.ExternalID, "cwd", s.CWD, "attempt", attempt, "reason", why, "err", err,
			"elapsed", time.Since(start))
		return false, true
	}
	if stillOpen {
		if err := cc.Close(ctx, s.ExternalID, false); err != nil {
			return fail("close failed", err)
		}
	}
	keep := worktreeHeldByLivePeer(sessions, s)
	if !finishPurge(ctx, cc, open, repoRoot, worktreeDir, s, keep) {
		return fail("worktree removal or purge did not complete", nil)
	}
	slog.Info("reconcile: purge_pending teardown completed",
		"session", s.ExternalID, "cwd", s.CWD, "attempt", attempt, "elapsed", time.Since(start))
	return true, true
}
