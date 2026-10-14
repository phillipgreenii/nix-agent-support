package parknote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/gitenv"
)

// Real-git guard for the discovery (V-3): the porcelain parsing, the primary
// branch resolution and the ahead count are properties of git itself, so they
// run against a throwaway repository, never a fake.

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitenv.Command(context.Background(), dir, args...)
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestBuild_realGitDrainWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	wt := filepath.Join(repo, ".worktrees", "b-1")
	git(t, repo, "worktree", "add", "-q", "-b", "drain/b-1", wt)
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "two")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second bead's branch, with no worktree and no commits.
	git(t, repo, "branch", "drain/b-2")

	note := Build(context.Background(), Input{Trigger: "t", Session: "s", Role: "drain", Bead: "b-1", Dirs: []string{repo}})
	for _, want := range []string{
		"worktree " + wt + " (branch drain/b-1): 2 commit(s) ahead of main; uncommitted changes: yes",
		"No person-question was identified",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("missing %q in:\n%s", want, note)
		}
	}
	if strings.Contains(note, "b-2") {
		t.Errorf("another bead's branch leaked into the note:\n%s", note)
	}

	// Branch only: remove the worktree, keep the branch.
	git(t, repo, "worktree", "remove", "--force", wt)
	note = Build(context.Background(), Input{Trigger: "t", Session: "s", Role: "drain", Bead: "b-1", Dirs: []string{repo}})
	if !strings.Contains(note, "branch drain/b-1 (no worktree): 2 commit(s) ahead of main") {
		t.Errorf("a branch whose worktree is gone must still be named:\n%s", note)
	}

	// A configured primary branch wins.
	git(t, repo, "branch", "trunk", "main")
	git(t, repo, "config", "pgii-integrate-branch.primaryBranch", "trunk")
	note = Build(context.Background(), Input{Trigger: "t", Session: "s", Role: "drain", Bead: "b-1", Dirs: []string{repo}})
	if !strings.Contains(note, "ahead of trunk") {
		t.Errorf("the configured primary branch must be used:\n%s", note)
	}
}
