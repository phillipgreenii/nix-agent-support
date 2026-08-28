package gh

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// leakedByAGitHookCommit is the GIT_* set a `git commit` FROM A LINKED
// WORKTREE actually exports into the hook environment (git 2.54.0; measured
// for pg-pr's pg2-lx41y, the structurally identical prior fix this one
// mirrors). Every descendant of the hook -- including a CETA process invoked
// from within it, or from a nested tool itself launched from one -- inherits
// these unless the child's environment is built explicitly.
var leakedByAGitHookCommit = []string{
	"GIT_DIR=/canonical/.git/worktrees/wt",
	"GIT_INDEX_FILE=/canonical/.git/worktrees/wt/index",
	"GIT_PREFIX=packages/claude-extended-tool-approver/",
	"GIT_EXEC_PATH=/nix/store/xxx-git/libexec/git-core",
	"GIT_AUTHOR_NAME=Someone",
	"GIT_AUTHOR_EMAIL=someone@example.com",
	"GIT_COMMITTER_NAME=Someone",
	"GIT_COMMITTER_EMAIL=someone@example.com",
}

// mustNotReachAChild lists, beyond the hook set above, every GIT_* variable
// that can redirect the child at a repository, an index, an object store, or
// a discovery boundary.
var mustNotReachAChild = []string{
	"GIT_WORK_TREE=/canonical",
	"GIT_COMMON_DIR=/canonical/.git",
	"GIT_OBJECT_DIRECTORY=/canonical/.git/objects",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=/elsewhere/objects",
	"GIT_CEILING_DIRECTORIES=/",
	"GIT_NAMESPACE=refs/namespaces/x",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
}

func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// TestHermeticGitEnviron_DropsEveryRepoRedirectingVar is the regression test
// the fix exists for: if any of these reaches the `git -C cwd rev-parse`
// child, `-C` is a lie and CurrentBranch answers with the leaked repository's
// branch instead of cwd's.
func TestHermeticGitEnviron_DropsEveryRepoRedirectingVar(t *testing.T) {
	base := slices.Concat(leakedByAGitHookCommit, mustNotReachAChild)
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	got := envKeys(hermeticGitEnviron())
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(got, k) {
			t.Errorf("hermeticGitEnviron leaked %q, which must never reach the git child", k)
		}
	}
}

// TestHermeticGitEnviron_PassesNonGitVarsThrough guards the other half of the
// contract: production git needs the ambient non-GIT_ environment (PATH,
// HOME, transport, locale), so filtering must not degrade into a full
// allowlist that breaks the resolver outright.
func TestHermeticGitEnviron_PassesNonGitVarsThrough(t *testing.T) {
	t.Setenv("CETA_RESOLVER_TEST_MARKER", "present")
	got := hermeticGitEnviron()
	if !slices.Contains(got, "CETA_RESOLVER_TEST_MARKER=present") {
		t.Fatalf("hermeticGitEnviron dropped a non-GIT_ variable it must pass through")
	}
	if !slices.Contains(got, "PATH="+os.Getenv("PATH")) {
		t.Fatalf("hermeticGitEnviron dropped PATH")
	}
}

// TestHermeticGitEnviron_KeepsAllowlistedGitVars asserts the allowlist half:
// a GIT_-prefixed variable naming a PROGRAM or config FILE (never a
// repository) still reaches the child, since callers rely on
// GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM to sandbox git in tests elsewhere in
// this codebase.
func TestHermeticGitEnviron_KeepsAllowlistedGitVars(t *testing.T) {
	for k := range inheritableGitVars {
		t.Setenv(k, "x")
	}
	got := hermeticGitEnviron()
	for k := range inheritableGitVars {
		if !slices.Contains(got, k+"=x") {
			t.Errorf("hermeticGitEnviron dropped allowlisted %q", k)
		}
	}
}

// initTestRepo creates a fresh git repo at a temp dir, checked out on branch,
// with one empty commit so HEAD resolves. The fixture's OWN git invocations
// use a minimal, explicit env (no ambient GIT_* forwarded) so they cannot be
// disturbed by the leak this test simulates around the call under test.
func initTestRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	fixtureEnv := []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = fixtureEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
	}
	run("init", "-q", "-b", branch)
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// TestExecBranchResolver_CurrentBranch_IgnoresLeakedGitDir drives the real
// production resolver (not a stub) with GIT_DIR/GIT_WORK_TREE naming a
// DIFFERENT repository than the -C argument -- exactly what a `git commit`
// from a linked worktree exports into every descendant process -- and asserts
// CurrentBranch resolves the directory it was HANDED, not the leaked one.
// Verified to FAIL against the pre-fix `cmd.Env` left nil (which inherits
// os.Environ() wholesale): it then reports "leaked-branch" for a call asking
// about target's branch.
func TestExecBranchResolver_CurrentBranch_IgnoresLeakedGitDir(t *testing.T) {
	target := initTestRepo(t, "target-branch")
	leaked := initTestRepo(t, "leaked-branch")

	t.Setenv("GIT_DIR", leaked+"/.git")
	t.Setenv("GIT_WORK_TREE", leaked)

	r := NewExecResolver()
	branch, err := r.CurrentBranch(target)
	if err != nil {
		t.Fatalf("CurrentBranch(%s) error: %v", target, err)
	}
	if branch != "target-branch" {
		t.Fatalf("CurrentBranch(%s) = %q; want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode -C", target, branch, "target-branch")
	}
}

// TestExecBranchResolver_CurrentBranch_StillWorksUnleaked pins the ordinary,
// no-leak path so the fix cannot be "pass by never resolving anything".
func TestExecBranchResolver_CurrentBranch_StillWorksUnleaked(t *testing.T) {
	dir := initTestRepo(t, "plain-branch")
	r := NewExecResolver()
	branch, err := r.CurrentBranch(dir)
	if err != nil {
		t.Fatalf("CurrentBranch(%s) error: %v", dir, err)
	}
	if branch != "plain-branch" {
		t.Fatalf("CurrentBranch(%s) = %q; want %q", dir, branch, "plain-branch")
	}
}
