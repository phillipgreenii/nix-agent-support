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

// initRepoForCLI sets up a minimal git repo (one commit on `main`) at dir
// with a github remote so `branch detect` produces a populated repo field.
func initRepoForCLI(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
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
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd2 := exec.Command("git", "-C", dir, "init")
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
