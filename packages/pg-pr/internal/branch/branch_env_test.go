package branch

import (
	"context"
	"path/filepath"
	"testing"
)

// leakGitLocationVars puts this process into the state a `git commit` from a
// LINKED WORKTREE puts every descendant of the pre-commit hook into: GIT_DIR
// and friends naming a repository that is NOT the directory the caller will
// pass. The values point at a path that is not a repository at all, so a call
// that inherits them fails loudly instead of reporting facts about the wrong
// clone.
//
// Before this package migrated onto x/gitclient (pg2-66340), the mechanism
// under test here was internal/gitenv (pg2-lx41y): git's repo discovery
// consults these vars BEFORE it looks at `-C dir`, so `-C dir` alone cannot
// override them, and gitenv.Command filtered the INHERITED process
// environment down to an allowlist. x/gitclient's Client goes further
// (design §4.4's environment contract): it BUILDS the child environment from
// scratch — PATH/HOME/SSH_AUTH_SOCK plus whatever an Option explicitly adds —
// so a var like GIT_DIR that was never passed through an Option has no path
// into the child at all, regardless of what this process's own environment
// contains. This test exercises that guarantee through the package's real
// production entry point (CLIGitRunner), the same way it did against the
// retired runGit helper.
func leakGitLocationVars(t *testing.T) {
	t.Helper()
	leaked := filepath.Join(t.TempDir(), "leaked-git-dir")
	t.Setenv("GIT_DIR", leaked)
	t.Setenv("GIT_WORK_TREE", leaked)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(leaked, "index"))
	t.Setenv("GIT_COMMON_DIR", leaked)
}

// TestCLIGitRunnerStaysInDirUnderLeakedGitDir covers the exported production
// path: CLIGitRunner's whole contract is "report facts about THIS
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
