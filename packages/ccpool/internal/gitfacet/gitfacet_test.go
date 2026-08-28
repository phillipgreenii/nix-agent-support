package gitfacet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitLocationEnvVars are the environment variables that override git's own
// repository discovery, consulted BEFORE `-C <dir>` or the process's cwd
// (git(1), ENVIRONMENT VARIABLES: "Repository", "Path"). TestMain below
// unsets them from this process's own environment once, before any test
// runs, so that neither runGit's fixture-building git calls nor the
// production git() calls exercised by these tests can be redirected at a
// leaked ambient repository -- e.g. one exported by a `git commit` pre-commit
// hook run from a linked worktree (pg2-67h4y), which is exactly the
// environment `go test` runs in during this module's own commit-time
// run-unit-tests hook. Belt-and-suspenders alongside production git()'s own
// per-call hermeticEnviron and runGit's own allowlisted env below: this
// scrub protects the ambient starting state; those protect each individual
// child regardless of it. Pattern: pg-pr's internal/worktree/main_test.go
// (pg2-12795, landed f04c2389).
var gitLocationEnvVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES",
	"GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY",
}

func TestMain(m *testing.M) {
	for _, k := range gitLocationEnvVars {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// runGit runs git in dir, failing the test on error. Used to build fixtures.
//
// The child's env is built by testGitEnv, an explicit ALLOWLIST -- never a
// full append(os.Environ(), ...). A fixture that builds another isolated
// repo is itself vulnerable to the exact ambient GIT_DIR/GIT_WORK_TREE leak
// this package's fix eliminates from production git() (pg2-aqpvr), and
// TestMain's scrub above only covers the process's STARTING state, not a
// later t.Setenv within an individual test (deliberately used below to
// simulate the leak).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

// testGitEnv returns a minimal, explicitly ALLOWLISTED environment for a git
// subprocess building a fixture in this package's tests: PATH/TMPDIR (to
// locate the git binary and its own scratch files) plus a fixed test
// identity, and nothing else from the ambient environment.
//
// HOME is pointed at its own fresh t.TempDir() rather than forwarded, unlike
// production hermeticEnviron (which must forward the real HOME so it keeps
// working against the operator's actual repos): this machine's real HOME
// wires a global git hooks path that refuses a commit whose author identity
// looks like a placeholder ("t"/"test"/...) -- exactly the identity this
// helper sets -- so forwarding it would make every fixture commit here fail
// (confirmed: TestResolve_normalCheckout et al. fail with
// "pg-git-check-identity: ... looks like a placeholder identity" against a
// forwarded ambient HOME, both before and independent of this package's
// GIT_DIR/GIT_WORK_TREE fix). GIT_CONFIG_NOSYSTEM/_GLOBAL/_SYSTEM back the
// isolation belt-and-suspenders. Mirrors pg-pr's
// internal/gitfixture.Env/CommitEnv (pg2-12795), which isolates HOME for the
// identical reason.
func testGitEnv(t *testing.T) []string {
	t.Helper()
	ambient := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			ambient[k] = v
		}
	}
	env := []string{
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
	for _, k := range []string{"PATH", "TMPDIR"} {
		if v, ok := ambient[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
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

// TestResolve_ignoresLeakedGitDir is the regression pin for pg2-aqpvr: git's
// repository discovery consults GIT_DIR/GIT_WORK_TREE before -C, so a
// `git commit` from a linked worktree that exports them into the process
// environment (mechanism write-up: pg2-67h4y) must not redirect Resolve --
// or the production git() call it drives -- at the leaked repository
// instead of the directory it was asked about. Verified to FAIL against the
// pre-fix code (git()'s exec.Command with no cmd.Env, which inherits
// os.Environ() wholesale): it then reports "leaked-branch" and the leaked
// repo's own toplevel for a call about target's facets.
func TestResolve_ignoresLeakedGitDir(t *testing.T) {
	gitAvailable(t)

	target := t.TempDir()
	target, _ = filepath.EvalSymlinks(target)
	runGit(t, target, "init", "-b", "target-branch")
	runGit(t, target, "commit", "--allow-empty", "-m", "init")

	leaked := t.TempDir()
	leaked, _ = filepath.EvalSymlinks(leaked)
	runGit(t, leaked, "init", "-b", "leaked-branch")
	runGit(t, leaked, "commit", "--allow-empty", "-m", "init")

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment (e.g. by an invoking git hook) pointing at a DIFFERENT
	// repository than the one Resolve is asked about.
	t.Setenv("GIT_DIR", filepath.Join(leaked, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked)

	f := Resolve(target)
	if f.Branch == nil || *f.Branch != "target-branch" {
		t.Fatalf("Branch = %v, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode -C %s",
			f.Branch, "target-branch", target)
	}
	if f.Worktree == nil || *f.Worktree != target {
		t.Fatalf("Worktree = %v, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode -C %s",
			f.Worktree, target, target)
	}
	if f.RepoRoot == nil || *f.RepoRoot != target {
		t.Fatalf("RepoRoot = %v, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode -C %s",
			f.RepoRoot, target, target)
	}
}

// TestGit_envExcludesLeakedGitDir asserts directly on the constructed
// cmd.Env (via hermeticEnviron, which git() uses): no GIT_-prefixed variable
// outside inheritableGitVars may reach the child.
func TestGit_envExcludesLeakedGitDir(t *testing.T) {
	t.Setenv("GIT_DIR", "/canonical/.git/worktrees/wt")
	t.Setenv("GIT_WORK_TREE", "/canonical")
	t.Setenv("GIT_INDEX_FILE", "/canonical/.git/worktrees/wt/index")
	t.Setenv("GIT_COMMON_DIR", "/canonical/.git")
	t.Setenv("GIT_PREFIX", "packages/ccpool/")
	t.Setenv("GIT_CEILING_DIRECTORIES", "/")
	t.Setenv("GIT_OBJECT_DIRECTORY", "/canonical/.git/objects")

	env := hermeticEnviron()
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(key, gitVarPrefix) {
			continue
		}
		if _, ok := inheritableGitVars[key]; !ok {
			t.Errorf("hermeticEnviron leaked %q, which must never reach the git child", key)
		}
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
