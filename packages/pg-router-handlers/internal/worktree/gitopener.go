package worktree

import (
	"context"

	"github.com/phillipgreenii/x/gitclient"
)

// GitOpener is the production Opener: gitclient.New adapted to Opener's
// covariant WorktreeManager return (mirrors internal/executor.Deps.gitOpener's
// identical adapter closure). It anchors directly at whatever dir it is given
// (closeUnlessNeedsInput passes s.CWD itself, never RepoRoot) -- proven
// equivalent to `git worktree remove` run from the repo root, since
// `git -C <worktree> worktree remove <worktree>` removes a linked worktree
// from within itself just as well. Tests call the teardown/reconcile helpers
// directly with a fake Opener instead of this default.
//
// RemoveWorktree on the returned manager is the paced removal (throttled.go):
// the tree is renamed into the trash and deleted in rate-limited batches
// instead of by one `git worktree remove` that unlinks every file at full speed.
//
// Shared by every adapter binary of this module; moved verbatim out of
// cmd/pg-router-handler-ccpool's gitWorktreeOpener.
var GitOpener Opener = ThrottledOpener(Removal{})

// PlainGitOpener is gitclient.New without the paced removal.
var PlainGitOpener Opener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
	return gitclient.New(ctx, dir)
}
