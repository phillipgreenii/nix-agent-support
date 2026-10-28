package parknote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/gitenv"
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

// Locate (bead pg2-b49ydt) picks a working directory from its answer, so each
// verdict is a property of real git: the porcelain parsing, the branch match, a
// worktree git still lists after its directory is gone, one repository reached
// through two checkouts.
func TestLocate_realGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkRepo := func(name string) string {
		repo := filepath.Join(root, name)
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "init", "-q", "-b", "main")
		git(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
		return repo
	}
	a, b := mkRepo("repo-a"), mkRepo("repo-b")
	ctx := context.Background()

	t.Run("none", func(t *testing.T) {
		loc, err := Locate(ctx, nil, "b-0", []string{a, b})
		if err != nil || len(loc.Worktrees) != 0 || len(loc.BranchOnly) != 0 {
			t.Fatalf("loc=%+v err=%v, want nothing found", loc, err)
		}
	})

	t.Run("one worktree, and a same-named directory on another branch is not it", func(t *testing.T) {
		wt := filepath.Join(a, ".worktrees", "b-1")
		git(t, a, "worktree", "add", "-q", "-b", "drain/b-1", wt)
		// The handler's own per-bead worktree for the same bead: not a drain worktree.
		git(t, b, "worktree", "add", "-q", "-b", "pg-router/b-1", filepath.Join(b, ".worktrees", "b-1"))
		loc, err := Locate(ctx, nil, "b-1", []string{a, b})
		if err != nil {
			t.Fatal(err)
		}
		if len(loc.Worktrees) != 1 || loc.Worktrees[0].Path != wt || loc.Worktrees[0].Repo != a {
			t.Fatalf("loc=%+v, want exactly %s found via %s", loc, wt, a)
		}
	})

	t.Run("the same repository reached through two checkouts counts once", func(t *testing.T) {
		wt := filepath.Join(a, ".worktrees", "b-1")
		loc, err := Locate(ctx, nil, "b-1", []string{a, wt})
		if err != nil {
			t.Fatal(err)
		}
		if len(loc.Worktrees) != 1 {
			t.Fatalf("loc=%+v, want one worktree", loc)
		}
	})

	t.Run("two repositories each holding one", func(t *testing.T) {
		git(t, a, "worktree", "add", "-q", "-b", "drain/b-2", filepath.Join(a, ".worktrees", "b-2"))
		git(t, b, "worktree", "add", "-q", "-b", "drain/b-2", filepath.Join(b, ".worktrees", "b-2"))
		loc, err := Locate(ctx, nil, "b-2", []string{a, b})
		if err != nil {
			t.Fatal(err)
		}
		if len(loc.Worktrees) != 2 {
			t.Fatalf("loc=%+v, want both reported", loc)
		}
		if d := loc.Describe(); !strings.Contains(d, filepath.Join(a, ".worktrees", "b-2")) || !strings.Contains(d, filepath.Join(b, ".worktrees", "b-2")) {
			t.Errorf("Describe must name both: %s", d)
		}
	})

	t.Run("branch with no worktree", func(t *testing.T) {
		git(t, b, "branch", "drain/b-3")
		loc, err := Locate(ctx, nil, "b-3", []string{a, b})
		if err != nil || len(loc.Worktrees) != 0 || len(loc.BranchOnly) != 1 || loc.BranchOnly[0] != b {
			t.Fatalf("loc=%+v err=%v, want branch-only in %s", loc, err, b)
		}
	})

	t.Run("a worktree whose directory is gone is not found", func(t *testing.T) {
		wt := filepath.Join(a, ".worktrees", "b-4")
		git(t, a, "worktree", "add", "-q", "-b", "drain/b-4", wt)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		loc, err := Locate(ctx, nil, "b-4", []string{a})
		if err != nil || len(loc.Worktrees) != 0 {
			t.Fatalf("loc=%+v err=%v, want no worktree", loc, err)
		}
	})

	t.Run("an unsearchable repository is an error, not absence", func(t *testing.T) {
		if _, err := Locate(ctx, nil, "b-5", []string{a, filepath.Join(root, "missing")}); err == nil {
			t.Fatal("want an error naming the repository")
		}
	})

	t.Run("it changes nothing", func(t *testing.T) {
		before := gitList(t, a)
		if _, err := Locate(ctx, nil, "b-1", []string{a, b}); err != nil {
			t.Fatal(err)
		}
		if after := gitList(t, a); after != before {
			t.Fatalf("worktree list changed:\n%s\nvs\n%s", before, after)
		}
	})
}

func gitList(t *testing.T, dir string) string {
	t.Helper()
	out, err := OSGit{}.Output(context.Background(), dir, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
