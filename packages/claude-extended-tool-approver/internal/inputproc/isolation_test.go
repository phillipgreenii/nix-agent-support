package inputproc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestGuardHostile(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"rtk git add -A", true},
		{"rtk git commit -m x", true},
		{"rtk git status", true},
		{"rtk rg -n git src", true},
		{"rtk grep -rn git-foo .", true},
		{"cd x && rtk git log", true},
		{"rtk --opt=/usr/bin/git status", true},
		{"rtk GIT.EXE status", true},
		{"rtk git@host status", true},
		{"rtk ls -la", false},
		{"rtk find . -name x", false},
		{"git status", false},
		{"/usr/bin/git status", false},
		{"rtk read README.md", false},
		{"rtk gitignore-x", false},
		{"rtk digit", false},
		// Unparseable: treated as hostile.
		{"rtk git 'unterminated", true},
	}
	for _, tc := range cases {
		if got := GuardHostile(tc.cmd); got != tc.want {
			t.Errorf("GuardHostile(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestInLinkedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gittest.New(t, gitfixture.RepoOptions{})
	if _, err := repo.Commit(t.Context(), "init", map[string]string{"README.md": "hi\n"}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	root := repo.Dir
	wt := filepath.Join(root, "wt")
	if out, err := repo.Client.Run(t.Context(), "-C", root, "worktree", "add", "-q", "-b", "feat", wt); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	sub := filepath.Join(wt, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rootSub := filepath.Join(root, "c")
	if err := os.MkdirAll(rootSub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		cwd  string
		want bool
	}{
		{"primary root", root, false},
		{"primary subdirectory", rootSub, false},
		{"linked worktree root", wt, true},
		{"linked worktree subdirectory", sub, true},
		{"empty cwd", "", false},
	} {
		if got := InLinkedWorktree(tc.cwd); got != tc.want {
			t.Errorf("%s: InLinkedWorktree(%q) = %v, want %v", tc.name, tc.cwd, got, tc.want)
		}
	}

	// A directory with no .git ancestor: a fresh temp dir (outside the repo).
	bare := t.TempDir()
	if got := InLinkedWorktree(bare); got {
		t.Errorf("no .git ancestor: InLinkedWorktree(%q) = true, want false", bare)
	}
}

func TestDropRewriteForIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gittest.New(t, gitfixture.RepoOptions{})
	if _, err := repo.Commit(t.Context(), "init", map[string]string{"README.md": "hi\n"}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	wt := filepath.Join(repo.Dir, "wt")
	if out, err := repo.Client.Run(t.Context(), "-C", repo.Dir, "worktree", "add", "-q", "-b", "feat", wt); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if !DropRewriteForIsolation(wt, "rtk git commit -m x") {
		t.Error("linked worktree + rtk git: want drop")
	}
	if DropRewriteForIsolation(wt, "rtk ls -la") {
		t.Error("linked worktree + rtk ls: want keep")
	}
	if DropRewriteForIsolation(repo.Dir, "rtk git commit -m x") {
		t.Error("primary checkout + rtk git: want keep")
	}
}
