package goldencorpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

func TestPathStateFixtureMaterializesGitFacts(t *testing.T) {
	fx := PathStateFixture{
		CWD: "/repo", ProjectRoot: "/repo",
		GitTracked: []string{"config/.env.local"}, Gitignored: []string{".env"}, WorktreeState: "dirty",
	}
	tree, err := fx.MaterializeTree()
	if err != nil {
		t.Fatal(err)
	}
	root := tree.ProjectRoot
	if tree.CWD != root || strings.HasPrefix(root, "/repo") {
		t.Fatalf("cwd %q root %q: want the throwaway repo for both", tree.CWD, root)
	}
	for _, p := range []string{".git/HEAD", ".git/hooks", "config/.env.local", ".env", ".gitignore"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if ig, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(ig) != "/.env\n" {
		t.Errorf(".gitignore = %q", ig)
	}
	if st, err := pathspec.ProbeWorktreeState(root); err != nil || st != pathspec.WorktreeDirty {
		t.Errorf("worktree state = %v, %v; want dirty", st, err)
	}
	// Cleanup removes the tree and forgets the registration.
	tree.Cleanup()
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("tree not removed: %v", err)
	}
	if st, _ := pathspec.ProbeWorktreeState(root); st == pathspec.WorktreeDirty {
		t.Error("registration outlived Cleanup")
	}
}

func TestPathStateFixtureRejectsEscapingAndBadState(t *testing.T) {
	for _, fx := range []PathStateFixture{
		{CWD: "/repo", GitTracked: []string{"../x"}},
		{CWD: "/repo", Gitignored: []string{"/abs"}},
		{CWD: "/repo", WorktreeState: "filthy"},
	} {
		if tree, err := fx.MaterializeTree(); err == nil {
			tree.Cleanup()
			t.Errorf("%+v: want an error", fx)
		}
	}
}

func TestPathStateFixtureNoFactsKeepsSyntheticPaths(t *testing.T) {
	tree, err := PathStateFixture{CWD: "/repo", ProjectRoot: "/repo"}.MaterializeTree()
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Cleanup()
	if tree.CWD != "/repo" || tree.ProjectRoot != "/repo" {
		t.Errorf("got %q %q", tree.CWD, tree.ProjectRoot)
	}
}

func TestPathStateFixtureScopePinsHome(t *testing.T) {
	t.Setenv("HOME", "/host/home")
	tree, err := PathStateFixture{CWD: "/repo", Home: "/home/user"}.MaterializeTree()
	if err != nil {
		t.Fatal(err)
	}
	done := tree.Scope()
	if got := os.Getenv("HOME"); got != "/home/user" {
		t.Errorf("HOME inside scope = %q", got)
	}
	done()
	if got := os.Getenv("HOME"); got != "/host/home" {
		t.Errorf("HOME after scope = %q", got)
	}
}
