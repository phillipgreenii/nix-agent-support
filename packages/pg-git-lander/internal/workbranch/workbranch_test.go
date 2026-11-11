package workbranch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

const opID = "20261107T101500Z-3f9c2a"

type fx struct {
	t    *testing.T
	repo *gitfixture.Repo
	g    *gitexec.Git
	b    *workbranch.Brancher
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
	return &fx{t: t, repo: repo, g: g, b: &workbranch.Brancher{Git: g}}
}

func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	r := f.g.Run(f.t.Context(), dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v in %s: %s", args, dir, r.Stderr)
	}
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) head(dir string) string { return f.git(dir, "rev-parse", "HEAD") }

func (f *fx) cur(dir string) string {
	r := f.g.Run(f.t.Context(), dir, "symbolic-ref", "--short", "-q", "HEAD")
	return strings.TrimSpace(string(r.Stdout))
}

// tipWith commits a new file on a side branch and returns that commit.
func (f *fx) tipWith(branch, file string) string {
	f.t.Helper()
	f.git(f.repo.Dir, "branch", branch)
	p := filepath.Join(f.repo.Root(), "side-"+branch)
	f.git(f.repo.Dir, "worktree", "add", p, branch)
	if err := os.WriteFile(filepath.Join(p, file), []byte("tracked\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(p, "add", file)
	f.git(p, "commit", "-m", "add "+file)
	oid := f.head(p)
	f.git(f.repo.Dir, "worktree", "remove", "--force", p)
	return oid
}

func (f *fx) chosen(dir string) *worktrees.Chosen {
	f.t.Helper()
	c := &worktrees.Chosen{Path: dir}
	if b := f.cur(dir); b != "" {
		c.Start = b
	} else {
		c.Start, c.StartDetached = f.head(dir), true
	}
	return c
}

func TestCreateFromBranchStartThenRestoreAndDelete(t *testing.T) {
	f := newFx(t)
	tip := f.tipWith("feat", "a.txt")
	c := f.chosen(f.repo.Dir)
	if err := f.b.Create(t.Context(), c, opID, tip); err != nil {
		t.Fatal(err)
	}
	if got := f.cur(f.repo.Dir); got != workbranch.Name(opID) {
		t.Fatalf("on %q, want work branch", got)
	}
	if f.head(f.repo.Dir) != tip {
		t.Fatal("work branch not at the given tip")
	}
	if err := f.b.Restore(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if got := f.cur(f.repo.Dir); got != "main" {
		t.Fatalf("restored to %q, want main", got)
	}
	if err := f.b.Delete(t.Context(), f.repo.Dir, opID); err != nil {
		t.Fatal(err)
	}
	if r := f.g.Run(t.Context(), f.repo.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+workbranch.Name(opID)); r.OK() {
		t.Fatal("work branch still exists")
	}
	if err := f.b.Delete(t.Context(), f.repo.Dir, opID); err != nil {
		t.Fatalf("deleting a missing branch: %v", err)
	}
}

func TestCreateFromDetachedStartRestoresOid(t *testing.T) {
	f := newFx(t)
	tip := f.tipWith("feat", "a.txt")
	start := f.head(f.repo.Dir)
	f.git(f.repo.Dir, "switch", "--detach", start)
	c := f.chosen(f.repo.Dir)
	if !c.StartDetached {
		t.Fatal("fixture should be detached")
	}
	if err := f.b.Create(t.Context(), c, opID, tip); err != nil {
		t.Fatal(err)
	}
	if f.cur(f.repo.Dir) != workbranch.Name(opID) {
		t.Fatal("not on work branch")
	}
	if err := f.b.RestoreAll(t.Context(), []*worktrees.Chosen{c}); err != nil {
		t.Fatal(err)
	}
	if f.cur(f.repo.Dir) != "" || f.head(f.repo.Dir) != start {
		t.Fatalf("not restored detached at %s", start)
	}
}

func TestCreateUntrackedCollisionIsSwitchRefused(t *testing.T) {
	f := newFx(t)
	tip := f.tipWith("feat", "clash.txt")
	if err := os.WriteFile(filepath.Join(f.repo.Dir, "clash.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := f.chosen(f.repo.Dir)
	err := f.b.Create(t.Context(), c, opID, tip)
	r, ok := err.(*worktrees.Refusal)
	if !ok || r.Reason != outcome.RefSwitchRefused || r.Path != "clash.txt" {
		t.Fatalf("err = %v, want switch-refused naming clash.txt", err)
	}
	if f.cur(f.repo.Dir) != "main" {
		t.Fatal("worktree not restored")
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo.Dir, "clash.txt")); string(b) != "mine\n" {
		t.Fatal("untracked file was touched")
	}
	if r := f.g.Run(t.Context(), f.repo.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+workbranch.Name(opID)); r.OK() {
		t.Fatal("work branch was left behind")
	}
}

func TestCheckNamespace(t *testing.T) {
	for _, tc := range []struct {
		branch, target string
		bad            bool
	}{
		{"feat", "main", false},
		{"feat", "", false},
		{"pg-git-lander", "main", true},
		{"pg-git-lander/x", "main", true},
		{"feat", "pg-git-lander", true},
		{"feat", "pg-git-lander/x", true},
		{"pg-git-landerx", "main", false},
	} {
		r := workbranch.CheckNamespace(tc.branch, tc.target)
		if (r != nil) != tc.bad {
			t.Fatalf("%+v: refusal = %v", tc, r)
		}
		if r != nil && r.Reason != outcome.RefNamespaceCollision {
			t.Fatalf("%+v: reason %s", tc, r.Reason)
		}
	}
}

func TestDestinationSwitchAwayFromAndBackToWorkBranch(t *testing.T) {
	f := newFx(t)
	tip := f.tipWith("feat", "a.txt")
	dw := f.chosen(f.repo.Dir) // on main: destination and work in one worktree
	if err := f.b.Create(t.Context(), dw, opID, tip); err != nil {
		t.Fatal(err)
	}
	if err := f.b.SwitchDestinationToTarget(t.Context(), dw, "main"); err != nil {
		t.Fatal(err)
	}
	if f.cur(f.repo.Dir) != "main" {
		t.Fatal("not back on TARGET")
	}
	if err := f.b.SwitchDestinationToWork(t.Context(), dw, opID); err != nil {
		t.Fatal(err)
	}
	if f.cur(f.repo.Dir) != workbranch.Name(opID) || f.head(f.repo.Dir) != tip {
		t.Fatal("not on the work branch again")
	}
}
