package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// leakGitLocationVars puts this process into the state a `git commit` from a
// LINKED WORKTREE puts every descendant of the pre-commit hook into: GIT_DIR
// and friends naming a repository that is NOT the directory the caller will
// pass. The values point at a path that is not a repository at all, so a call
// that inherits them fails loudly instead of silently mutating a real clone.
//
// Before this package migrated onto x/gitclient (pg2-3sl0t), the mechanism
// under test here was internal/gitenv (pg2-lx41y): git's repo discovery
// consults these vars BEFORE it looks at `-C dir`, so `-C dir` alone cannot
// override them, and gitenv.Command filtered the INHERITED process
// environment down to an allowlist. x/gitclient's Client goes further
// (design §4.4's environment contract): it BUILDS the child environment from
// scratch — PATH/HOME/SSH_AUTH_SOCK plus whatever an Option explicitly adds —
// so a var like GIT_DIR that was never passed through an Option has no path
// into the child at all, regardless of what this process's own environment
// contains. This test suite exercises that guarantee through this package's
// real production entry points (CLIGitClient), the same way it did against
// the retired runGit helper.
func leakGitLocationVars(t *testing.T) {
	t.Helper()
	leaked := filepath.Join(t.TempDir(), "leaked-git-dir")
	t.Setenv("GIT_DIR", leaked)
	t.Setenv("GIT_WORK_TREE", leaked)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(leaked, "index"))
	t.Setenv("GIT_COMMON_DIR", leaked)
}

// TestWorktreeInfoStaysInDirUnderLeakedGitDir drives CLIGitClient.WorktreeInfo
// — the read-only probe both List and Remove rely on — under a leaked
// ambient GIT_DIR/GIT_WORK_TREE/etc, and asserts it reports the repo actually
// at path rather than silently redirecting onto the (nonexistent) leaked
// location.
func TestWorktreeInfoStaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	leakGitLocationVars(t)

	wt, err := NewCLIGitClient().WorktreeInfo(context.Background(), repo)
	if err != nil {
		t.Fatalf("WorktreeInfo inherited the leaked GIT_DIR and could not resolve %s: %v", repo, err)
	}
	if wt.Branch != "main" {
		t.Fatalf("WorktreeInfo resolved the wrong repository: got branch %q, want %q", wt.Branch, "main")
	}
}

// TestCreateWorktreeStaysInDirUnderLeakedGitDir drives
// CLIGitClient.CreateWorktree — a MUTATING verb — under the same leaked
// environment, and asserts the new worktree is actually created off repo
// rather than the leaked location.
func TestCreateWorktreeStaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	leakGitLocationVars(t)

	target := filepath.Join(t.TempDir(), "wt")
	if err := NewCLIGitClient().CreateWorktree(context.Background(), repo, target, "leak-test", ""); err != nil {
		t.Fatalf("CreateWorktree inherited the leaked GIT_DIR and failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatalf("worktree not created inside target %s: %v", target, err)
	}
}
