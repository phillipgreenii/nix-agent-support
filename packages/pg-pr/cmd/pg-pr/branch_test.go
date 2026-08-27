package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitfixture"
)

// initRepoForCLI sets up a minimal git repo (one commit on `main`) at dir
// with a github remote so `branch detect` produces a populated repo field.
//
// Every git call here goes through internal/gitfixture's allowlisted,
// hermetic environment (pg2-12795) so that no fixture can touch a real git
// repo/config by construction, even if this whole `go test` process was
// itself invoked from inside a git hook that leaked GIT_DIR/GIT_WORK_TREE
// into its own environment.
func initRepoForCLI(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		gitfixture.MustRun(t, dir, args...)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := gitfixture.Run(t, dir, "init", "-b", "main"); err != nil {
		if out2, err2 := gitfixture.Run(t, dir, "init"); err2 != nil {
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
