package branch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ----------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------

// hermeticEnviron builds a MINIMAL, EXPLICITLY ALLOWLISTED environment for
// the git subprocesses these fixtures shell out to, so that no fixture here
// can touch a real git repo/config BY CONSTRUCTION.
//
// `-C <dir>` only changes the working directory before git runs; it does NOT
// override GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_CEILING_DIRECTORIES,
// or GIT_COMMON_DIR — vars git's own repo discovery consults FIRST and which
// -C cannot override. If any of those leak into the environment that
// launched `go test` (a git hook — pre-commit/prek — exports exactly these
// for the commit in progress, and `go test` inherits that environment when
// it is run AS the commit-time hook, as pg-test-runner's run-unit-tests hook
// does for this module), every "isolated" `-C <fixture>` call below silently
// redirects onto whatever repository those variables name instead. This is
// the exact mechanism that corrupted the CANONICAL clone's own .git/config
// (core.worktree + user.email/user.name) — see pg2-12795 / pg2-5ek6b — and
// the identical bug class already fixed the same way in
// claude-extended-tool-approver's hermeticEnviron (pg2-8wnhc / pg2-rrhw2):
// see that package's primarycommit_worktree_test.go for the full writeup.
//
// Fixed by inverting denylist to allowlist: the subprocess environment is
// built by ADDING only what git demonstrably needs for these local,
// no-network operations (init/config/commit/checkout), instead of
// SUBTRACTING known-dangerous vars — a name this list doesn't yet know about
// (forgotten today, or invented by a future git release) is excluded
// automatically. HOME is pointed at a fresh t.TempDir() (not the ambient
// value) so even a fallback this allowlist hasn't anticipated lands in an
// empty per-test directory, never the real user's home.
func hermeticEnviron(t *testing.T) []string {
	t.Helper()
	ambient := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			ambient[k] = v
		}
	}
	env := []string{"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	// PATH: to locate the git binary and anything it execs. TMPDIR: git's own
	// scratch files. GIT_CONFIG_GLOBAL/_SYSTEM: forwarded so a caller's
	// t.Setenv override reaches the subprocess. None of these four names a
	// git repository location.
	for _, k := range []string{"PATH", "TMPDIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if v, ok := ambient[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// initRepo creates a git repo at dir with one commit on `main`.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(
			hermeticEnviron(t),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "init", "-b", "main")
	cmd.Env = hermeticEnviron(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		// fallback for older git
		cmd2 := exec.Command("git", "-C", dir, "init")
		cmd2.Env = hermeticEnviron(t)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			t.Fatalf("git init: %v\n%s\n%s", err, out, out2)
		}
		run("symbolic-ref", "HEAD", "refs/heads/main")
	}
	run("config", "user.name", "test")
	run("config", "user.email", "test@example.com")
	run("config", "commit.gpgsign", "false")

	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
}

func addRemote(t *testing.T, dir, url string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", url)
	cmd.Env = hermeticEnviron(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

func checkoutBranch(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "checkout", "-b", branch)
	cmd.Env = hermeticEnviron(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b %s: %v\n%s", branch, err, out)
	}
}

// ----------------------------------------------------------------------
// Fakes
// ----------------------------------------------------------------------

// fakeGH returns a configurable response from PRForBranch.
type fakeGH struct {
	number *int
	err    error
}

func (f *fakeGH) PRForBranch(_ context.Context, _ string) (*int, error) {
	return f.number, f.err
}

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

func TestDetectBasicWithPR(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	addRemote(t, dir, "git@github.com:owner/repo.git")
	checkoutBranch(t, dir, "feat/x")

	pr := 17
	got, err := Detect(ctx, dir, Options{GH: &fakeGH{number: &pr}})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got.Repo != "owner/repo" {
		t.Fatalf("repo: got %q want owner/repo", got.Repo)
	}
	if got.Branch != "feat/x" {
		t.Fatalf("branch: got %q want feat/x", got.Branch)
	}
	if got.Base != "origin/main" {
		t.Fatalf("base: got %q want origin/main", got.Base)
	}
	// Worktree root resolves to the real, evaluated path (macOS /private/var
	// vs /var symlinks, etc.). Compare via EvalSymlinks.
	wantRoot, _ := filepath.EvalSymlinks(dir)
	gotRoot, _ := filepath.EvalSymlinks(got.WorktreeRoot)
	if wantRoot != gotRoot {
		t.Fatalf("worktree_root: got %q want %q", got.WorktreeRoot, dir)
	}
	if got.PRNumber == nil || *got.PRNumber != 17 {
		t.Fatalf("pr_id: got %v want 17", got.PRNumber)
	}
}

func TestDetectNoPRIsNotFatal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	addRemote(t, dir, "https://github.com/owner/repo.git")

	got, err := Detect(ctx, dir, Options{
		GH: &fakeGH{number: nil, err: errors.New("no pull requests found")},
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got.PRNumber != nil {
		t.Fatalf("expected nil PRNumber, got %v", *got.PRNumber)
	}
	if got.Branch != "main" {
		t.Fatalf("branch: got %q want main", got.Branch)
	}
	if got.Repo != "owner/repo" {
		t.Fatalf("repo: got %q want owner/repo", got.Repo)
	}
}

func TestDetectGHUnavailableDegrades(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	addRemote(t, dir, "git@github.com:owner/repo.git")

	// GH==nil means use default CLI client. With an unrealistic PATH it
	// would fail — but we want the package-level guarantee that *any*
	// error from PRForBranch degrades to nil rather than blowing up. Use
	// a fake that simulates "gh not on PATH".
	got, err := Detect(ctx, dir, Options{
		GH: &fakeGH{err: errors.New(`exec: "gh": executable file not found in $PATH`)},
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got.PRNumber != nil {
		t.Fatalf("expected nil PRNumber when gh unavailable, got %v", *got.PRNumber)
	}
}

func TestDetectOutsideGitRepoErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir() // not a git repo

	_, err := Detect(ctx, dir, Options{GH: &fakeGH{}})
	if err == nil {
		t.Fatalf("expected error outside a git repo")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("error msg: got %q want contains %q", err.Error(), "not in a git repository")
	}
}

func TestDetectNonGitHubRemoteEmptyRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	addRemote(t, dir, "https://example.com/owner/repo.git")

	got, err := Detect(ctx, dir, Options{GH: &fakeGH{}})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	// Non-GitHub remote: Repo is best-effort empty rather than fatal.
	if got.Repo != "" {
		t.Fatalf("repo: got %q want empty for non-github remote", got.Repo)
	}
	if got.Branch != "main" {
		t.Fatalf("branch: got %q want main", got.Branch)
	}
}

func TestDetectMissingRemoteEmptyRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir) // no remote configured

	got, err := Detect(ctx, dir, Options{GH: &fakeGH{}})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got.Repo != "" {
		t.Fatalf("repo: got %q want empty when remote missing", got.Repo)
	}
}
