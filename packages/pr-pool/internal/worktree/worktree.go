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
)

// errNotARepo is returned by the production Git when a path is not a worktree
// root (rev-parse fails). Tests reuse it via the recGit fake.
var errNotARepo = errors.New("not a git worktree")

// Git runs `git -C <dir> <args...>` (injectable; matches watchdog.GitRunner so
// the executor can share OSGit). A nil error means the command succeeded.
type Git interface {
	Run(ctx context.Context, dir string, args ...string) error
}

// Ensure returns the path to a fresh per-bead worktree, creating it under
// worktreeDir off repoRoot's current HEAD on branch pr-pool/<beadID>. If a
// worktree already exists at that path it is reused (idempotent). The branch name
// and dir derive deterministically from beadID, so a redispatch reuses the same
// isolated workspace rather than the shared monorepo checkout.
func Ensure(ctx context.Context, git Git, worktreeDir, repoRoot, beadID string) (string, error) {
	path := filepath.Join(worktreeDir, beadID)
	// Reuse: if the path is already a worktree root, keep it.
	if err := git.Run(ctx, path, "rev-parse", "--is-inside-work-tree"); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktree dir: %w", err)
	}
	branch := "pr-pool/" + beadID
	// -B resets/creates the branch at HEAD; addressing repoRoot so the new worktree
	// branches off the monorepo's current commit, then checks out in isolation.
	if err := git.Run(ctx, repoRoot, "worktree", "add", "-B", branch, path); err != nil {
		return "", fmt.Errorf("worktree add %s: %w", path, err)
	}
	return path, nil
}
