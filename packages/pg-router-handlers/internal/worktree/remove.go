package worktree

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

// DefaultRemoveTimeout is the deadline of one `git worktree remove` when the
// caller has no configured value (config.Config.WorktreeRemoveTimeout carries
// the same default). It is deliberately far longer than the deadlines of the
// surrounding passes (the sweep's, a purge-close's): removing a checkout of
// tens of thousands of files takes minutes, and a removal killed half-way is
// the expensive failure, not a removal that runs long.
const DefaultRemoveTimeout = 10 * time.Minute

// healPruneTimeout bounds the registration prune that follows a killed removal.
const healPruneTimeout = time.Minute

// RemoveWithin runs wm.RemoveWorktree(path, force) under its OWN deadline d
// (<= 0: DefaultRemoveTimeout) instead of the surrounding context's. The
// parent's deadline does not apply to the removal (a pass whose own bound is a
// minute must not kill a removal that needs five), but the parent's
// cancellation does: a handler told to stop is not held for the whole removal.
//
// A removal that hits d is killed by git's own context handling, which can leave
// the checkout half deleted. The kill must not strand the bead's path in a
// state nothing heals. git deletes the `.git` file during the work-tree delete,
// and it prunes the admin directory only after the delete finishes, so a kill
// can leave the admin directory still naming a path whose `.git` is gone: that
// path is neither a linked worktree (no `.git`) nor an unregistered leftover
// (git still registers it), so neither the worktree sweep nor the own-bead
// heal would touch it. After such a kill, prune (when non-nil) runs on a fresh
// short context, anchored at the repository root, to drop the stale
// registration; the half-deleted directory is then the unregistered leftover
// the reclaim already deletes.
func RemoveWithin(parent context.Context, wm gitclient.WorktreeManager, path string, force bool, d time.Duration, prune func(ctx context.Context) error) error {
	if d <= 0 {
		d = DefaultRemoveTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), d)
	defer cancel()
	stop := context.AfterFunc(parent, func() {
		if errors.Is(parent.Err(), context.Canceled) {
			cancel()
		}
	})
	defer stop()

	err := wm.RemoveWorktree(ctx, path, force)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	slog.Warn("worktree removal hit its deadline and was killed; healing the registration",
		"worktree", path, "deadline", d, "err", err)
	healKilledRemoval(parent, path, prune)
	return err
}

// healKilledRemoval prunes the stale registration of a half-deleted worktree
// whose `.git` file is already gone. A worktree that still has its `.git` entry
// is left alone: git still knows it, and the next guarded removal retries it.
func healKilledRemoval(parent context.Context, path string, prune func(ctx context.Context) error) {
	if prune == nil {
		return
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), healPruneTimeout)
	defer cancel()
	if err := prune(ctx); err != nil {
		slog.Warn("worktree removal heal: prune failed; the stale registration stays until the next prune",
			"worktree", path, "err", err)
	}
}

// PruneVia returns a prune function that opens repoRoot with open and runs
// `git worktree prune` there (RemoveWithin's heal hook). The repository root
// is used, not the worktree, because the half-deleted worktree directory can no
// longer anchor a git client.
func PruneVia(open Opener, repoRoot string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		root, err := open(ctx, repoRoot)
		if err != nil {
			return err
		}
		return root.PruneWorktrees(ctx)
	}
}
