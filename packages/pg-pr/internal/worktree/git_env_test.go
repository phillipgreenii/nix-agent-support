package worktree

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// leakGitLocationVars puts this process into the state a `git commit` from a
// LINKED WORKTREE puts every descendant of the pre-commit hook into: GIT_DIR
// and friends naming a repository that is NOT the directory the caller will
// pass. The values point at a path that is not a repository at all, so a call
// that inherits them fails loudly instead of silently mutating a real clone.
//
// See internal/gitenv for the mechanism (pg2-lx41y): git's repo discovery
// consults these BEFORE it looks at `-C dir`, so `-C dir` cannot override
// them.
func leakGitLocationVars(t *testing.T) {
	t.Helper()
	leaked := filepath.Join(t.TempDir(), "leaked-git-dir")
	t.Setenv("GIT_DIR", leaked)
	t.Setenv("GIT_WORK_TREE", leaked)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(leaked, "index"))
	t.Setenv("GIT_COMMON_DIR", leaked)
}

// TestRunGitStaysInDirUnderLeakedGitDir drives the package's real runGit —
// the same helper every mutating verb here goes through (worktree add,
// worktree remove, fetch, config) — and asserts it resolved the directory it
// was handed rather than the leaked repository.
func TestRunGitStaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	leakGitLocationVars(t)

	out, err := runGit(context.Background(), repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatalf("runGit inherited the leaked GIT_DIR and could not resolve %s: %v", repo, err)
	}

	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		resolved = repo
	}
	want := filepath.Join(resolved, ".git")
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("runGit resolved the wrong repository\n got: %s\nwant: %s", got, want)
	}
}
