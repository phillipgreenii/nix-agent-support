package session

import (
	"os"
	"path/filepath"
	"testing"
)

func makeRepo(t *testing.T, head string) string {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGitBranchNamedBranch(t *testing.T) {
	dir := makeRepo(t, "ref: refs/heads/my-feature")
	got := GitBranch(dir)
	if got != "my-feature" {
		t.Errorf("GitBranch = %q, want \"my-feature\"", got)
	}
}

func TestGitBranchDetachedHead(t *testing.T) {
	sha := "abc1234def5678901234567890123456789012ab"
	dir := makeRepo(t, sha)
	got := GitBranch(dir)
	if got != "abc1234" {
		t.Errorf("GitBranch = %q, want \"abc1234\"", got)
	}
}

func TestGitBranchNoRepo(t *testing.T) {
	dir := t.TempDir()
	got := GitBranch(dir)
	if got != "" {
		t.Errorf("GitBranch = %q, want \"\"", got)
	}
}

func TestGitBranchSubdirectory(t *testing.T) {
	root := makeRepo(t, "ref: refs/heads/main")
	sub := filepath.Join(root, "pkg", "foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := GitBranch(sub)
	if got != "main" {
		t.Errorf("GitBranch = %q, want \"main\"", got)
	}
}

func TestGitBranchWorktree(t *testing.T) {
	// Simulate a worktree: main repo at root, worktree at wt/
	root := t.TempDir()
	mainGit := filepath.Join(root, ".git")
	worktreesDir := filepath.Join(mainGit, "worktrees", "feat")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreesDir, "HEAD"), []byte("ref: refs/heads/feat/new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Worktree dir: .git is a file pointing to worktrees/feat
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := "gitdir: " + worktreesDir
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(gitFile+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := GitBranch(wt)
	if got != "feat/new" {
		t.Errorf("GitBranch = %q, want \"feat/new\"", got)
	}
}

func TestGitBranchWorktreeSubdirectory(t *testing.T) {
	root := t.TempDir()
	mainGit := filepath.Join(root, ".git")
	worktreesDir := filepath.Join(mainGit, "worktrees", "feat")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreesDir, "HEAD"), []byte("ref: refs/heads/feat/new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+worktreesDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Session cwd is a subdirectory of the worktree
	sub := filepath.Join(wt, "src", "components")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := GitBranch(sub)
	if got != "feat/new" {
		t.Errorf("GitBranch = %q, want \"feat/new\"", got)
	}
}
