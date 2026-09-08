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
	"io/fs"
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
//
// pg2-an65v: the probe must ALSO tolerate fs.ErrNotExist, not only
// gitclient.ErrNotARepository. gitclient.New's doc comment promises
// ErrNotARepository whenever dir is not inside a git repository, but its actual
// implementation resolves symlinks (filepath.EvalSymlinks) BEFORE ever running
// git, so a path that does not exist ON DISK yet — the normal case for a
// bead's worktree the very first time it is dispatched — fails at that
// resolution step with a plain wrapped "no such file or directory" error, never
// reaching the code that would wrap it as ErrNotARepository. Before this fix,
// Ensure treated that as a hard failure and never attempted `git worktree add`
// at all, so EVERY first-time dispatch failed here unconditionally (confirmed
// empirically against the real gitclient.New — the prior fix, a mocked Opener
// in this package's own tests, could not have caught it because it never
// exercised gitclient's real error shape). This was the actual cause of the
// 100%-failure-rate launches investigated in pg2-an65v; it reproduces
// regardless of whether pr-pool itself runs from the canonical clone or a
// linked worktree, which refutes that bead's nested-worktree hypothesis as the
// mechanism (a fresh path fails identically either way).
func Ensure(ctx context.Context, open Opener, worktreeDir, repoRoot, beadID string) (string, error) {
	path := filepath.Join(worktreeDir, beadID)
	// Reuse: if the path is already a worktree root, keep it. A path that does
	// not exist yet is never an existing worktree — treat that the same as the
	// intended not-a-repository signal. Any OTHER error is a real failure (e.g.
	// a canceled context) and must propagate rather than be swallowed into an
	// attempted create.
	if _, err := open(ctx, path); err == nil {
		return path, nil
	} else if !errors.Is(err, gitclient.ErrNotARepository) && !errors.Is(err, fs.ErrNotExist) {
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
