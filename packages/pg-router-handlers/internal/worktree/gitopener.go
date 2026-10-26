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
// Shared by every adapter binary of this module (bead pg2-12gr4.10.3); moved
// verbatim out of cmd/pg-router-handler-ccpool's gitWorktreeOpener.
var GitOpener Opener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
	return gitclient.New(ctx, dir)
}
