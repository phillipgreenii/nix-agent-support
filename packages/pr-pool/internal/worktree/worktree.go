// Package worktree assigns a fresh, isolated per-bead git worktree at dispatch so
// a pr-pool worker never runs on whatever unrelated branch the monorepo happens
// to be on (pg2-yukh root cause #2). The worktree dir is <worktreeDir>/<beadID>
// on a dedicated branch pr-pool/<beadID>; it is idempotent (an existing worktree
// for the bead is reused).
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/x/gitclient"
)

// Opener anchors a gitclient at dir, returning the WorktreeManager role Ensure
// needs both to probe reuse and to create a fresh worktree. Production wires
// this to gitclient.New; tests substitute a fake so they never touch a real
// repo (design §4.6's app-local opener seam for multi-directory consumers —
// Ensure opens both the prospective worktree path and repoRoot).
type Opener func(ctx context.Context, dir string) (gitclient.WorktreeManager, error)

// Ensure returns the path to a fresh per-bead worktree, creating it under
// worktreeDir off repoRoot's current HEAD on branch pr-pool/<beadID>. If a
// worktree already exists at that path it is reused (idempotent) — detected via
// open(ctx, path)'s gitclient.ErrNotARepository sentinel rather than a
// `rev-parse --is-inside-work-tree` probe. The branch name and dir derive
// deterministically from beadID, so a redispatch reuses the same isolated
// workspace rather than the shared monorepo checkout.
func Ensure(ctx context.Context, open Opener, worktreeDir, repoRoot, beadID string) (string, error) {
	path := filepath.Join(worktreeDir, beadID)
	// Reuse: if the path is already a worktree root, keep it. Any error other
	// than "not a repository yet" is a real failure (e.g. a canceled context)
	// and must propagate rather than be swallowed into an attempted create.
	if _, err := open(ctx, path); err == nil {
		return path, nil
	} else if !errors.Is(err, gitclient.ErrNotARepository) {
		return "", fmt.Errorf("probe worktree %s: %w", path, err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktree dir: %w", err)
	}
	branch := "pr-pool/" + beadID
	wm, err := open(ctx, repoRoot)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", repoRoot, err)
	}
	// ResetBranch (-B) resets/creates the branch at HEAD; anchored at repoRoot so
	// the new worktree branches off the monorepo's current commit, then checks
	// out in isolation.
	if err := wm.CreateWorktree(ctx, path, branch, gitclient.CreateWorktreeOptions{ResetBranch: true}); err != nil {
		return "", fmt.Errorf("worktree add %s: %w", path, err)
	}
	return path, nil
}
