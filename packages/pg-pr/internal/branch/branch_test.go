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

// initRepo creates a git repo at dir with one commit on `main`.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(
			os.Environ(),
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
		// fallback for older git
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
}

func addRemote(t *testing.T, dir, url string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", url)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

func checkoutBranch(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "checkout", "-b", branch)
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
