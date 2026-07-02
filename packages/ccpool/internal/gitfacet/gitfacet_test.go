package gitfacet

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// runGit runs git in dir, failing the test on error. Used to build fixtures.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

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
	gitAvailable(t)
	dir := t.TempDir()
	// macOS TempDir may live under /var -> /private/var; canonicalize so the
	// fixture path matches git's --show-toplevel output.
	dir, _ = filepath.EvalSymlinks(dir)
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")

	f := Resolve(dir)
	if f.RepoRoot == nil || *f.RepoRoot != dir {
		t.Errorf("RepoRoot = %v, want %q", f.RepoRoot, dir)
	}
	if f.Worktree == nil || *f.Worktree != dir {
		t.Errorf("Worktree = %v, want %q", f.Worktree, dir)
	}
	if f.Branch == nil || *f.Branch != "main" {
		t.Errorf("Branch = %v, want \"main\"", f.Branch)
	}
}

// A linked worktree: worktree is the linked dir, repo root is the MAIN repo,
// so RepoRoot != Worktree.
func TestResolve_linkedWorktree(t *testing.T) {
	gitAvailable(t)
	main := t.TempDir()
	main, _ = filepath.EvalSymlinks(main)
	runGit(t, main, "init", "-b", "main")
	runGit(t, main, "commit", "--allow-empty", "-m", "init")

	wt := filepath.Join(t.TempDir(), "linked")
	runGit(t, main, "worktree", "add", "-b", "feature", wt)
	wt, _ = filepath.EvalSymlinks(wt)

	f := Resolve(wt)
	if f.Worktree == nil || *f.Worktree != wt {
		t.Errorf("Worktree = %v, want linked %q", f.Worktree, wt)
	}
	if f.RepoRoot == nil || *f.RepoRoot != main {
		t.Errorf("RepoRoot = %v, want MAIN repo %q", f.RepoRoot, main)
	}
	if f.Branch == nil || *f.Branch != "feature" {
		t.Errorf("Branch = %v, want \"feature\"", f.Branch)
	}
}

// Detached HEAD: branch is nil (not the literal "HEAD"); the other facets resolve.
func TestResolve_detachedHEAD(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	runGit(t, dir, "checkout", "--detach", "HEAD")

	f := Resolve(dir)
	if f.Branch != nil {
		t.Errorf("Branch = %v, want nil on detached HEAD", f.Branch)
	}
	if f.RepoRoot == nil || f.Worktree == nil {
		t.Errorf("detached HEAD should still resolve root/worktree, got %+v", f)
	}
}
