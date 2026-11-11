package worktrees_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

type fx struct {
	t    *testing.T
	repo *gitfixture.Repo
	sel  *worktrees.Selector
}

func newFx(t *testing.T, opts gitfixture.RepoOptions) *fx {
	t.Helper()
	repo := gittest.New(t, opts)
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
	return &fx{t: t, repo: repo, sel: &worktrees.Selector{Git: g}}
}

func (f *fx) git(dir string, args ...string) {
	f.t.Helper()
	r := f.sel.Git.Run(f.t.Context(), dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v in %s: %s", args, dir, r.Stderr)
	}
}

// addWT adds a linked worktree on a new branch and returns its path.
func (f *fx) addWT(name, branch string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), name)
	f.git(f.repo.Dir, "worktree", "add", "-b", branch, p)
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fx) mkBranch(name string) { f.t.Helper(); f.git(f.repo.Dir, "branch", name) }

func (f *fx) sub(wt, name string) string {
	f.t.Helper()
	p := filepath.Join(wt, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) do(in worktrees.Input) (*worktrees.Selection, error) {
	return f.sel.Select(f.t.Context(), in)
}

func wantRefusal(t *testing.T, err error, reason outcome.RefusalReason) *worktrees.Refusal {
	t.Helper()
	var r *worktrees.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want refusal %s", err, reason)
	}
	if r.Reason != reason {
		t.Fatalf("refusal = %v, want %s", r, reason)
	}
	return r
}

func roles(c *worktrees.Chosen) string {
	s := ""
	for _, r := range c.Roles {
		s += string(r) + ","
	}
	return s
}

func TestCascadeBranchAndTargetCheckedOut(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	feat := f.addWT("feat", "feat")
	sel, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Destination == nil || sel.Destination.Path != f.repo.Dir || !sel.Destination.IsMain || sel.DestinationFallback {
		t.Fatalf("destination = %+v", sel.Destination)
	}
	if sel.Source.Path != feat || !sel.Source.OnBranch || !sel.SourceDerived || sel.Work != sel.Source {
		t.Fatalf("source/work = %+v / %+v", sel.Source, sel.Work)
	}
	if roles(sel.Source) != "source,work," || roles(sel.Destination) != "destination," {
		t.Fatalf("roles = %s | %s", roles(sel.Source), roles(sel.Destination))
	}
	if sel.Source.AdminName != "feat" || sel.Destination.AdminName != worktrees.MainAdminName {
		t.Fatalf("admin names = %q, %q", sel.Source.AdminName, sel.Destination.AdminName)
	}
	if sel.Source.Start != "feat" || sel.Source.StartDetached || len(sel.Worktrees) != 2 || len(sel.Warnings) != 0 {
		t.Fatalf("source start/worktrees/warnings = %+v %d %v", sel.Source, len(sel.Worktrees), sel.Warnings)
	}
}

func TestCascadeOneWorktreeDoesEverythingWarns(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	f.mkBranch("feat")
	sel, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Source != sel.Destination || sel.Work != sel.Source || len(sel.Worktrees) != 1 {
		t.Fatalf("want one worktree for all roles, got %d", len(sel.Worktrees))
	}
	if roles(sel.Source) != "source,destination,work," && roles(sel.Source) != "destination,source,work," {
		t.Fatalf("roles = %s", roles(sel.Source))
	}
	if sel.Source.OnBranch {
		t.Fatal("worktree was not on BRANCH")
	}
	if len(sel.Warnings) != 1 || sel.Warnings[0] != outcome.WarnWorkInDestination {
		t.Fatalf("warnings = %v", sel.Warnings)
	}
}

func TestWorkWorktreeFlagAndNoWarningElsewhere(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	f.mkBranch("feat")
	spare := f.addWT("spare", "spare")
	sel, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir, WorkFlag: spare})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Work.Path != spare || sel.Source.Path != f.repo.Dir || len(sel.Warnings) != 0 {
		t.Fatalf("work = %s source = %s warnings = %v", sel.Work.Path, sel.Source.Path, sel.Warnings)
	}
}

func TestTargetCheckedOutNowhereUsesFlagThenCwdAsFallbackRoot(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	feat := f.addWT("feat", "feat")
	spare := f.addWT("spare", "spare")
	f.mkBranch("dev")

	sel, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "dev", Cwd: f.repo.Dir, TargetFlag: spare})
	if err != nil {
		t.Fatal(err)
	}
	if sel.TargetCheckedOut || sel.Destination.Path != spare || !sel.DestinationFallback || sel.Source.Path != feat {
		t.Fatalf("flag fallback: %+v", sel)
	}

	sel, err = f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "dev", Cwd: spare})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Destination.Path != spare || !sel.DestinationFallback {
		t.Fatalf("cwd fallback: %+v", sel.Destination)
	}

	// A fallback-only destination is not checked: dirty spare does not refuse.
	if err := os.WriteFile(filepath.Join(spare, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(spare, "add", "README")
	f.git(spare, "commit", "-m", "readme")
	if err := os.WriteFile(filepath.Join(spare, "README"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "dev", Cwd: f.repo.Dir, TargetFlag: spare}); err != nil {
		t.Fatalf("fallback-only destination was checked: %v", err)
	}
}

func TestLandNothingFoundIsNoWorktree(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	f.mkBranch("feat")
	f.mkBranch("dev")
	gitDir := filepath.Join(f.repo.Dir, ".git")
	_, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "dev", Cwd: gitDir})
	wantRefusal(t, err, outcome.RefNoWorktree)
}

func TestRebaseCascade(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	feat := f.addWT("feat", "feat")
	f.mkBranch("loose")
	spare := f.addWT("spare", "spare")

	// BRANCH checked out: SW is that worktree, no DW.
	sel, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "feat", Cwd: f.repo.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Destination != nil || sel.Source.Path != feat || sel.Work != sel.Source {
		t.Fatalf("rebase checked out: %+v", sel)
	}

	// BRANCH nowhere: the current directory (a subdirectory counts).
	sub := f.sub(spare, "pkg")
	sel, err = f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "loose", Cwd: sub})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Destination != nil || sel.Source.Path != spare || sel.Work.Path != spare || sel.SourceDerived {
		t.Fatalf("rebase cwd fallback: %+v", sel.Source)
	}

	// A path that is not a worktree: no-worktree.
	_, err = f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "loose", Cwd: filepath.Join(f.repo.Dir, ".git")})
	wantRefusal(t, err, outcome.RefNoWorktree)

	// --source-worktree simply names the SW.
	sel, err = f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "loose", Cwd: filepath.Join(f.repo.Dir, ".git"), SourceFlag: spare})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Source.Path != spare || !sel.SourceExplicit {
		t.Fatalf("explicit source: %+v", sel.Source)
	}
}

func TestMismatchAndNotAWorktree(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	f.addWT("feat", "feat")
	spare := f.addWT("spare", "spare")
	f.mkBranch("loose")
	f.mkBranch("dev")
	outside := t.TempDir()

	_, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir, SourceFlag: spare})
	wantRefusal(t, err, outcome.RefSourceWTMismatch)

	_, err = f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir, TargetFlag: spare})
	wantRefusal(t, err, outcome.RefTargetWTMismatch)

	r := func(in worktrees.Input) *worktrees.Refusal {
		_, err := f.do(in)
		return wantRefusal(t, err, outcome.RefNotAWorktree)
	}
	if got := r(worktrees.Input{Op: worktrees.Land, Branch: "loose", Target: "main", Cwd: f.repo.Dir, SourceFlag: outside}); got.Role != worktrees.Source {
		t.Fatalf("role = %s", got.Role)
	}
	if got := r(worktrees.Input{Op: worktrees.Land, Branch: "loose", Target: "main", Cwd: f.repo.Dir, WorkFlag: outside}); got.Role != worktrees.Work {
		t.Fatalf("role = %s", got.Role)
	}
	if got := r(worktrees.Input{Op: worktrees.Land, Branch: "loose", Target: "dev", Cwd: f.repo.Dir, TargetFlag: outside}); got.Role != worktrees.Destination {
		t.Fatalf("role = %s", got.Role)
	}
}

func TestDirtyAndForeignRebaseAndSecondCall(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	feat := f.addWT("feat", "feat")
	in := worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir}

	if _, err := f.do(in); err != nil {
		t.Fatal(err)
	}

	// Tracked change appears while the tool waited: the second call refuses.
	if err := os.WriteFile(filepath.Join(feat, "f.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(feat, "add", "f.txt")
	f.git(feat, "commit", "-m", "f")
	if err := os.WriteFile(filepath.Join(feat, "f.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.do(in)
	r := wantRefusal(t, err, outcome.RefDirty)
	if r.Role != worktrees.Source || r.Detail != "" || r.Path != feat {
		t.Fatalf("refusal = %+v", r)
	}
	f.git(feat, "checkout", "--", "f.txt")

	// An untracked file is not dirt.
	if err := os.WriteFile(filepath.Join(feat, "untracked"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.do(in); err != nil {
		t.Fatalf("untracked file refused: %v", err)
	}

	// A foreign rebase.
	gd := filepath.Join(f.repo.Dir, ".git", "worktrees", "feat", "rebase-merge")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = f.do(in)
	r = wantRefusal(t, err, outcome.RefDirty)
	if r.Detail != worktrees.DetailForeignRebase {
		t.Fatalf("detail = %q", r.Detail)
	}
	// A record that accounts for it lifts the refusal.
	in.Accounted = func(p string) bool { return p == feat }
	if _, err := f.do(in); err != nil {
		t.Fatalf("accounted rebase refused: %v", err)
	}
}

func TestDirtyDestinationNamesRole(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	f.addWT("feat", "feat")
	if err := os.WriteFile(filepath.Join(f.repo.Dir, "m.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo.Dir, "add", "m.txt")
	f.git(f.repo.Dir, "commit", "-m", "m")
	if err := os.WriteFile(filepath.Join(f.repo.Dir, "m.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir})
	r := wantRefusal(t, err, outcome.RefDirty)
	if r.Role != worktrees.Destination {
		t.Fatalf("role = %s", r.Role)
	}
}

func TestCwdWouldBeRemoved(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	feat := f.addWT("feat", "feat")
	sub := f.sub(feat, "deep/er")
	base := worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main"}

	for _, cwd := range []string{feat, sub} {
		in := base
		in.Cwd = cwd
		_, err := f.do(in)
		r := wantRefusal(t, err, outcome.RefCwdWouldBeRemoved)
		if r.Path != feat {
			t.Fatalf("path = %s", r.Path)
		}

		in.KeepSourceWorktree = true
		if _, err := f.do(in); err != nil {
			t.Fatalf("keep flag: %v", err)
		}
		in.KeepSourceWorktree = false
		in.SourceFlag = feat // explicit consent
		if _, err := f.do(in); err != nil {
			t.Fatalf("explicit source: %v", err)
		}
	}

	in := base
	in.Cwd = f.repo.Dir // another path
	if _, err := f.do(in); err != nil {
		t.Fatalf("other cwd: %v", err)
	}

	// A rebase removes nothing.
	if _, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "feat", Cwd: sub}); err != nil {
		t.Fatalf("rebase: %v", err)
	}
}

func TestMainWorktreeRemoval(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	f.addWT("other", "other")
	f.mkBranch("dev")
	other := filepath.Join(f.repo.Root(), "other")
	// main worktree is on main = BRANCH; judged from git, not from the name.
	in := worktrees.Input{Op: worktrees.Land, Branch: "main", Target: "dev", Cwd: other}
	_, err := f.do(in)
	wantRefusal(t, err, outcome.RefMainWorktreeRemoval)

	in.SourceFlag = f.repo.Dir // explicit does not help
	_, err = f.do(in)
	wantRefusal(t, err, outcome.RefMainWorktreeRemoval)

	in.KeepSourceWorktree = true
	if _, err := f.do(in); err != nil {
		t.Fatalf("keep flag: %v", err)
	}

	// A rebase removes nothing.
	if _, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "main", Cwd: other}); err != nil {
		t.Fatalf("rebase: %v", err)
	}

	// Main reached only by the cascade (scenario 4) is restored, not removed.
	f.mkBranch("feat")
	if _, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir}); err != nil {
		t.Fatalf("cascade main: %v", err)
	}
}

func TestMainWorktreeIsFoundFromGitNotBranchNames(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true, InitialBranch: "trunk"})
	other := f.addWT("other", "main") // a worktree on a branch called main
	f.mkBranch("dev")
	_, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "main", Target: "dev", Cwd: f.repo.Dir})
	if err != nil {
		t.Fatalf("non-main worktree on branch main: %v", err)
	}
	_ = other
	_, err = f.do(worktrees.Input{Op: worktrees.Land, Branch: "trunk", Target: "dev", Cwd: other})
	wantRefusal(t, err, outcome.RefMainWorktreeRemoval)
}

func TestUnsupportedLayouts(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		f := newFx(t, gitfixture.RepoOptions{Bare: true})
		_, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "main", Cwd: f.repo.Dir})
		r := wantRefusal(t, err, outcome.RefUnsupportedLayout)
		if r.Detail != "bare" {
			t.Fatalf("detail = %q", r.Detail)
		}
	})
	t.Run("core.worktree", func(t *testing.T) {
		f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
		f.git(f.repo.Dir, "config", "core.worktree", f.repo.Dir)
		_, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "main", Cwd: f.repo.Dir})
		r := wantRefusal(t, err, outcome.RefUnsupportedLayout)
		if r.Detail != "core.worktree" {
			t.Fatalf("detail = %q", r.Detail)
		}
	})
	t.Run("sparse checkout in the work worktree", func(t *testing.T) {
		f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
		feat := f.addWT("feat", "feat")
		spare := f.addWT("spare", "spare")
		f.git(spare, "sparse-checkout", "init")
		in := worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir}
		// Sparse in a worktree that is not the work worktree is fine.
		if _, err := f.do(in); err != nil {
			t.Fatalf("sparse elsewhere: %v", err)
		}
		in.WorkFlag = spare
		_, err := f.do(in)
		r := wantRefusal(t, err, outcome.RefUnsupportedLayout)
		if r.Role != worktrees.Work {
			t.Fatalf("role = %s", r.Role)
		}
		_ = feat
	})
	t.Run("separate git dir is supported", func(t *testing.T) {
		f := newFx(t, gitfixture.RepoOptions{InitialCommit: true, SeparateGitDir: "gitdir/repo.git"})
		feat := f.addWT("feat", "feat")
		sel, err := f.do(worktrees.Input{Op: worktrees.Land, Branch: "feat", Target: "main", Cwd: f.repo.Dir})
		if err != nil {
			t.Fatal(err)
		}
		if !sel.Destination.IsMain || sel.Destination.AdminName != worktrees.MainAdminName ||
			sel.Destination.Path != f.repo.Dir || sel.Source.Path != feat || sel.Source.AdminName != "feat" {
			t.Fatalf("selection = %+v / %+v", sel.Destination, sel.Source)
		}
		// From a linked worktree, a rebase of its branch does not need main.
		if sel, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "feat", Cwd: feat}); err != nil || sel.Source.Path != feat {
			t.Fatalf("rebase from linked worktree: %v", err)
		}
	})
}

func TestAdminNameIsGitsNotThePathBasename(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	// Two worktrees whose directories share a basename get git's own
	// distinct admin names (feat, feat1).
	a := f.addWT("feat", "a")
	b := filepath.Join(f.repo.Root(), "sub", "feat")
	f.git(f.repo.Dir, "worktree", "add", "-b", "b", b)
	selA, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "a", Cwd: a})
	if err != nil {
		t.Fatal(err)
	}
	selB, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "b", Cwd: b})
	if err != nil {
		t.Fatal(err)
	}
	if selA.Source.AdminName != "feat" || selB.Source.AdminName != "feat1" {
		t.Fatalf("admin names = %q, %q", selA.Source.AdminName, selB.Source.AdminName)
	}
}

func TestDetachedStartState(t *testing.T) {
	f := newFx(t, gitfixture.RepoOptions{InitialCommit: true})
	spare := f.addWT("spare", "spare")
	f.git(spare, "checkout", "--detach")
	f.mkBranch("loose")
	sel, err := f.do(worktrees.Input{Op: worktrees.Rebase, Branch: "loose", Cwd: spare})
	if err != nil {
		t.Fatal(err)
	}
	if !sel.Source.StartDetached || len(sel.Source.Start) != 40 || sel.Source.OnBranch {
		t.Fatalf("start = %+v", sel.Source)
	}
}
