package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
// no-network operations (init/config/commit), instead of SUBTRACTING
// known-dangerous vars — a name this list doesn't yet know about (forgotten
// today, or invented by a future git release) is excluded automatically.
// HOME is pointed at a fresh t.TempDir() (not the ambient value) so even a
// fallback this allowlist hasn't anticipated lands in an empty per-test
// directory, never the real user's home.
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

// initRepoForCLI sets up a minimal git repo (one commit on `main`) at dir
// with a github remote so `branch detect` produces a populated repo field.
func initRepoForCLI(t *testing.T, dir string) {
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
	run("remote", "add", "origin", "git@github.com:owner/repo.git")
}

// TestBranchDetectHuman verifies the human-readable output emits one
// `key: value` line per field. The test runs in an isolated PATH so `gh`
// is unavailable, forcing the PR field to `null`.
func TestBranchDetectHuman(t *testing.T) {
	tmp := t.TempDir()
	initRepoForCLI(t, tmp)
	t.Chdir(tmp)
	// Hide gh from PATH so PR detection deterministically returns null.
	t.Setenv("PATH", filepath.Dir(mustLookPath(t, "git")))

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"branch", "detect"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"repo: owner/repo",
		"branch: main",
		"base: origin/main",
		"worktree_root: ",
		"pr_id: null",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

// TestBranchDetectJSON verifies --json emits a valid BranchInfo object.
func TestBranchDetectJSON(t *testing.T) {
	tmp := t.TempDir()
	initRepoForCLI(t, tmp)
	t.Chdir(tmp)
	t.Setenv("PATH", filepath.Dir(mustLookPath(t, "git")))

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"branch", "detect", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var parsed struct {
		Repo         string `json:"repo"`
		Branch       string `json:"branch"`
		Base         string `json:"base"`
		WorktreeRoot string `json:"worktree_root"`
		PRNumber     *int   `json:"pr_id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if parsed.Repo != "owner/repo" {
		t.Fatalf("repo: %q", parsed.Repo)
	}
	if parsed.Branch != "main" {
		t.Fatalf("branch: %q", parsed.Branch)
	}
	if parsed.Base != "origin/main" {
		t.Fatalf("base: %q", parsed.Base)
	}
	if parsed.PRNumber != nil {
		t.Fatalf("expected null pr_id, got %v", *parsed.PRNumber)
	}
}

// TestBranchDetectEnvJSON verifies PGPR_OUTPUT=json (without --json flag)
// selects JSON output. Covers the global env-var fallback (A15).
func TestBranchDetectEnvJSON(t *testing.T) {
	tmp := t.TempDir()
	initRepoForCLI(t, tmp)
	t.Chdir(tmp)
	t.Setenv("PATH", filepath.Dir(mustLookPath(t, "git")))
	t.Setenv("PGPR_OUTPUT", "json")
	// Reset the flag in case a prior test left it true (cobra does not
	// reset bool flags between Execute() calls on a shared rootCmd).
	brFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"branch", "detect"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var parsed struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected JSON from PGPR_OUTPUT=json, got: %q\nerr=%v",
			stdout.String(), err)
	}
	if parsed.Repo != "owner/repo" {
		t.Fatalf("repo: %q", parsed.Repo)
	}
}

// TestBranchDetectFlagBeatsEnv verifies that --json wins over a contradictory
// PGPR_OUTPUT value (defensive: confirms precedence rule).
func TestBranchDetectFlagBeatsEnv(t *testing.T) {
	tmp := t.TempDir()
	initRepoForCLI(t, tmp)
	t.Chdir(tmp)
	t.Setenv("PATH", filepath.Dir(mustLookPath(t, "git")))
	t.Setenv("PGPR_OUTPUT", "yaml") // not "json"
	brFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"branch", "detect", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") {
		t.Fatalf("expected JSON object from --json, got: %q", stdout.String())
	}
}

// TestBranchDetectOutsideGitRepoFails verifies that running detect from a
// non-git directory returns a clear error.
func TestBranchDetectOutsideGitRepoFails(t *testing.T) {
	tmp := t.TempDir() // not a git repo
	t.Chdir(tmp)
	t.Setenv("PATH", filepath.Dir(mustLookPath(t, "git")))

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"branch", "detect"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error outside git repo, got stdout=%q", stdout.String())
	} else if !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustLookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("lookpath %s: %v", name, err)
	}
	return p
}
