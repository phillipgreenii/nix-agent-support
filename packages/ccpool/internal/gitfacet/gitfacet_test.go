package gitfacet

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// Fixtures are built through gittest/gitfixture (bead pg2-svfbb's design,
// "gittest -- the isolated-repo fixture") rather than a hand-rolled
// exec.Command helper: the fixture is hermetic by
// CONSTRUCTION (isolated HOME, GIT_CEILING_DIRECTORIES, no system config, no
// hooks -- see gitfixture's guarantees), so there is no longer a need for
// this file's own allowlisted testGitEnv/runGit plumbing, nor for TestMain to
// scrub ambient GIT_* vars before tests run: gitclient's child environment is
// built from an explicit allowlist (PATH/HOME/SSH_AUTH_SOCK) and never
// inherits GIT_DIR/GIT_WORK_TREE/etc from the test process's environment
// regardless of what it contains (pg2-svfbb.8, migrating pg2-aqpvr's
// hand-rolled hermeticEnviron fix onto x/gitclient).

// Outside a git work tree, all facets are absent (nil) and never error.
func TestResolve_outsideRepo(t *testing.T) {
	dir := t.TempDir()
	f := Resolve(dir)
	if f.RepoRoot != nil || f.Worktree != nil || f.Branch != nil {
		t.Errorf("outside a repo all facets must be nil, got %+v", f)
	}
}

// A normal checkout: repo root == worktree, branch is the checked-out branch.
func TestResolve_normalCheckout(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "main"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}

	f := Resolve(repo.Dir)
	if f.RepoRoot == nil || *f.RepoRoot != repo.Dir {
		t.Errorf("RepoRoot = %v, want %q", f.RepoRoot, repo.Dir)
	}
	if f.Worktree == nil || *f.Worktree != repo.Dir {
		t.Errorf("Worktree = %v, want %q", f.Worktree, repo.Dir)
	}
	if f.Branch == nil || *f.Branch != "main" {
		t.Errorf("Branch = %v, want \"main\"", f.Branch)
	}
}

// A linked worktree: worktree is the linked dir, repo root is the MAIN repo,
// so RepoRoot != Worktree.
func TestResolve_linkedWorktree(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "main"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "linked")
	if err := repo.Client.CreateWorktree(t.Context(), wt, "feature", gitclient.CreateWorktreeOptions{}); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	wt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", wt, err)
	}

	f := Resolve(wt)
	if f.Worktree == nil || *f.Worktree != wt {
		t.Errorf("Worktree = %v, want linked %q", f.Worktree, wt)
	}
	if f.RepoRoot == nil || *f.RepoRoot != repo.Dir {
		t.Errorf("RepoRoot = %v, want MAIN repo %q", f.RepoRoot, repo.Dir)
	}
	if f.Branch == nil || *f.Branch != "feature" {
		t.Errorf("Branch = %v, want \"feature\"", f.Branch)
	}
}

// TestResolve_ignoresLeakedGitDir is the regression pin for pg2-aqpvr: git's
// repository discovery consults GIT_DIR/GIT_WORK_TREE before -C (or, as of
// this migration, before the client's cmd.Dir anchor), so a `git commit` from
// a linked worktree that exports them into the process environment
// (mechanism write-up: pg2-67h4y) must not redirect Resolve -- or the
// gitclient.Client it drives -- at the leaked repository instead of the
// directory it was asked about. Now proven via x/gitclient's own env
// allowlist (bead pg2-svfbb's design, "The client -- gitclient/client.go")
// rather than this package's own hermeticEnviron, which no longer exists.
func TestResolve_ignoresLeakedGitDir(t *testing.T) {
	target := gittest.New(t, gitfixture.RepoOptions{Suite: "target", InitialBranch: "target-branch"})
	if _, err := target.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}

	leaked := gittest.New(t, gitfixture.RepoOptions{Suite: "leaked", InitialBranch: "leaked-branch"})
	if _, err := leaked.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment (e.g. by an invoking git hook) pointing at a DIFFERENT
	// repository than the one Resolve is asked about.
	t.Setenv("GIT_DIR", filepath.Join(leaked.Dir, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked.Dir)

	f := Resolve(target.Dir)
	if f.Branch == nil || *f.Branch != "target-branch" {
		t.Fatalf("Branch = %v, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode target %s",
			f.Branch, "target-branch", target.Dir)
	}
	if f.Worktree == nil || *f.Worktree != target.Dir {
		t.Fatalf("Worktree = %v, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode target %s",
			f.Worktree, target.Dir, target.Dir)
	}
	if f.RepoRoot == nil || *f.RepoRoot != target.Dir {
		t.Fatalf("RepoRoot = %v, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode target %s",
			f.RepoRoot, target.Dir, target.Dir)
	}
}

// Detached HEAD: branch is nil (not the literal "HEAD"); the other facets resolve.
func TestResolve_detachedHEAD(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "main"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.Client.Run(t.Context(), "checkout", "--detach", "HEAD"); err != nil {
		t.Fatalf("checkout --detach: %v", err)
	}

	f := Resolve(repo.Dir)
	if f.Branch != nil {
		t.Errorf("Branch = %v, want nil on detached HEAD", f.Branch)
	}
	if f.RepoRoot == nil || f.Worktree == nil {
		t.Errorf("detached HEAD should still resolve root/worktree, got %+v", f)
	}
}
