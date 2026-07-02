package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ----------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------

// initRepo creates a bare-like git repo at dir with an initial commit on
// `main`, and configures user.name / user.email so commits succeed in
// hermetic CI environments.
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
	// Use -b main; if old git, fall back.
	cmd := exec.Command("git", "-C", dir, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback for older git versions.
		cmd2 := exec.Command("git", "-C", dir, "init")
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			t.Fatalf("git init: %v\n%s\n%s", err, out, out2)
		}
		run("symbolic-ref", "HEAD", "refs/heads/main")
	}
	run("config", "user.name", "test")
	run("config", "user.email", "test@example.com")
	run("config", "commit.gpgsign", "false")

	// Make an initial commit so HEAD exists and worktree add can find
	// a starting point.
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
}

// configureRemote sets remote.origin.url to a github-looking URL so
// RepoFromRemote succeeds. It does not create a real remote.
func configureRemote(t *testing.T, dir, url string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", url)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

// createOriginPRRef forges a ref refs/remotes/origin/pr/<n> pointing at HEAD,
// so `git worktree add ... origin/pr/<n>` succeeds without a real network
// fetch.
func createOriginPRRef(t *testing.T, dir string, pr int) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "update-ref",
		fmt.Sprintf("refs/remotes/origin/pr/%d", pr), "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v\n%s", err, out)
	}
}

// ----------------------------------------------------------------------
// Fakes
// ----------------------------------------------------------------------

// fakeGH always reports the PR exists.
type fakeGH struct {
	missing map[int]bool
}

func (f *fakeGH) PRExists(_ context.Context, _, _ string, pr int) (*PRInfo, error) {
	if f.missing[pr] {
		return nil, fmt.Errorf("PR #%d not found", pr)
	}
	return &PRInfo{Number: pr}, nil
}

// noFetchGitClient wraps the real CLIGitClient but replaces FetchPR with
// a no-op (the test already forged origin/pr/<n> via update-ref).
type noFetchGitClient struct {
	GitClient
}

func (n *noFetchGitClient) FetchPR(_ context.Context, _ string, _ int) error { return nil }

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

func TestAddRemoveListRoundtrip(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	wtRoot := filepath.Join(tmp, "reviews")

	initRepo(t, repoDir)
	configureRemote(t, repoDir, "git@github.com:owner/repo.git")
	createOriginPRRef(t, repoDir, 42)
	createOriginPRRef(t, repoDir, 7)

	opts := Options{
		WorktreeRoot: wtRoot,
		RepoDir:      repoDir,
		Git:          &noFetchGitClient{GitClient: NewCLIGitClient()},
		GH:           &fakeGH{},
	}

	// 1. List on empty root -> empty.
	got, err := List(ctx, opts)
	if err != nil {
		t.Fatalf("list (empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %+v", got)
	}

	// 2. Add PR 42.
	addRes, err := Add(ctx, 42, opts)
	if err != nil {
		t.Fatalf("add 42: %v", err)
	}
	if addRes.AlreadyExists {
		t.Fatalf("first add should not be already-exists")
	}
	if addRes.Branch != "review/pr-42" {
		t.Fatalf("branch: got %q", addRes.Branch)
	}
	if _, err := os.Stat(addRes.Path); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	// 3. Add PR 42 again -> already exists.
	addRes2, err := Add(ctx, 42, opts)
	if err != nil {
		t.Fatalf("add 42 (second): %v", err)
	}
	if !addRes2.AlreadyExists {
		t.Fatalf("second add should report already-exists")
	}

	// 4. Add PR 7.
	if _, err := Add(ctx, 7, opts); err != nil {
		t.Fatalf("add 7: %v", err)
	}

	// 5. List -> two entries, sorted by PR number.
	got, err = List(ctx, opts)
	if err != nil {
		t.Fatalf("list (populated): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %+v", got)
	}
	if got[0].PRNumber != 7 || got[1].PRNumber != 42 {
		t.Fatalf("sort order: %+v", got)
	}
	if got[0].Branch != "review/pr-7" {
		t.Fatalf("branch on listed worktree: %q", got[0].Branch)
	}

	// 6. Remove PR 7 (clean) -> Removed=true.
	rmRes, err := Remove(ctx, 7, opts)
	if err != nil {
		t.Fatalf("remove 7: %v", err)
	}
	if !rmRes.Removed {
		t.Fatalf("expected Removed=true, got %+v", rmRes)
	}
	if _, err := os.Stat(rmRes.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present: %v", err)
	}

	// 7. Remove PR 7 again -> Skipped (no such worktree).
	rmRes2, err := Remove(ctx, 7, opts)
	if err != nil {
		t.Fatalf("remove 7 (second): %v", err)
	}
	if !rmRes2.Skipped {
		t.Fatalf("expected Skipped=true on missing worktree, got %+v", rmRes2)
	}
}

func TestRemoveRefusesDirtyWithoutForce(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	wtRoot := filepath.Join(tmp, "reviews")

	initRepo(t, repoDir)
	configureRemote(t, repoDir, "https://github.com/owner/repo.git")
	createOriginPRRef(t, repoDir, 99)

	opts := Options{
		WorktreeRoot: wtRoot,
		RepoDir:      repoDir,
		Git:          &noFetchGitClient{GitClient: NewCLIGitClient()},
		GH:           &fakeGH{},
	}

	if _, err := Add(ctx, 99, opts); err != nil {
		t.Fatalf("add 99: %v", err)
	}

	// Dirty the worktree.
	dirty := filepath.Join(wtRoot, "pr-99", "DIRTY.txt")
	if err := os.WriteFile(dirty, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without --force: skipped.
	rmRes, err := Remove(ctx, 99, opts)
	if err != nil {
		t.Fatalf("remove 99 (no force): %v", err)
	}
	if !rmRes.Skipped {
		t.Fatalf("expected Skipped, got %+v", rmRes)
	}
	if !strings.Contains(rmRes.SkipReason, "uncommitted") {
		t.Fatalf("skip reason: %q", rmRes.SkipReason)
	}

	// With --force: removed.
	opts.Force = true
	rmRes, err = Remove(ctx, 99, opts)
	if err != nil {
		t.Fatalf("remove 99 (force): %v", err)
	}
	if !rmRes.Removed {
		t.Fatalf("expected Removed, got %+v", rmRes)
	}
}

func TestAddRefusesUnknownPR(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	wtRoot := filepath.Join(tmp, "reviews")

	initRepo(t, repoDir)
	configureRemote(t, repoDir, "git@github.com:owner/repo.git")

	opts := Options{
		WorktreeRoot: wtRoot,
		RepoDir:      repoDir,
		Git:          &noFetchGitClient{GitClient: NewCLIGitClient()},
		GH:           &fakeGH{missing: map[int]bool{1234: true}},
	}

	if _, err := Add(ctx, 1234, opts); err == nil {
		t.Fatalf("expected error for unknown PR")
	}

	// Worktree directory must not exist on failure.
	if _, err := os.Stat(filepath.Join(wtRoot, "pr-1234")); err == nil {
		t.Fatalf("worktree dir created for unknown PR")
	}
}

func TestListSkipsNonPRDirs(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	wtRoot := filepath.Join(tmp, "reviews")
	if err := os.MkdirAll(filepath.Join(wtRoot, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtRoot, "pr-abc"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := List(ctx, Options{WorktreeRoot: wtRoot})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero entries, got %+v", got)
	}
}

func TestListMissingRootIsEmpty(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	got, err := List(ctx, Options{WorktreeRoot: filepath.Join(tmp, "does-not-exist")})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestRepoFromRemoteParsesGitHubURLs(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		url         string
		wantOwner   string
		wantRepo    string
		expectError bool
	}{
		{"git@github.com:owner/repo.git", "owner", "repo", false},
		{"https://github.com/owner/repo.git", "owner", "repo", false},
		{"https://github.com/owner/repo", "owner", "repo", false},
		{"https://example.com/owner/repo.git", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			tmp := t.TempDir()
			initRepo(t, tmp)
			configureRemote(t, tmp, tc.url)
			owner, repo, err := NewCLIGitClient().RepoFromRemote(ctx, tmp)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error for url %q", tc.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Fatalf("got %q/%q want %q/%q", owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

func TestOptionsRequireWorktreeRoot(t *testing.T) {
	ctx := context.Background()
	if _, err := List(ctx, Options{}); err == nil {
		t.Fatalf("expected error when WorktreeRoot is empty")
	}
}
