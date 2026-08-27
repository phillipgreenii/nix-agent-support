package branch

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
// that inherits them fails loudly instead of reporting facts about the wrong
// clone.
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
// the helper behind Detect's toplevel/branch/remote lookups — and asserts it
// resolved the directory it was handed rather than the leaked repository.
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

// TestCLIGitRunnerStaysInDirUnderLeakedGitDir covers the exported production
// path too: CLIGitRunner's whole contract is "report facts about THIS
// directory", which a leaked GIT_DIR silently breaks.
func TestCLIGitRunnerStaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	checkoutBranch(t, repo, "feature-x")
	leakGitLocationVars(t)

	g := NewCLIGitRunner()

	branch, err := g.CurrentBranch(context.Background(), repo)
	if err != nil {
		t.Fatalf("CurrentBranch inherited the leaked GIT_DIR: %v", err)
	}
	if branch != "feature-x" {
		t.Fatalf("branch: got %q, want %q (resolved the wrong repository)", branch, "feature-x")
	}

	root, err := g.WorktreeRoot(context.Background(), repo)
	if err != nil {
		t.Fatalf("WorktreeRoot inherited the leaked GIT_DIR: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		resolved = repo
	}
	if root != resolved {
		t.Fatalf("WorktreeRoot: got %q, want %q (resolved the wrong repository)", root, resolved)
	}
}
