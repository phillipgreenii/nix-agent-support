package cleanup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cleanup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/landmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

const opID = "20261107T101500Z-3f9c2a"

type fx struct {
	t      *testing.T
	repo   *gitfixture.Repo
	g      *gitexec.Git
	c      *cleanup.Cleaner
	target string
	base   string
	// tips is BRANCH's tip, result the work branch's tip (the landed commit).
	tips, result string
	sw           *worktrees.Chosen
	dw           *worktrees.Chosen
}

// newFx builds a landed state: main (TARGET, checked out in the main
// worktree) has been fast-forwarded to result; the source worktree "sw" was
// on feat and now sits on the work branch.
func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	env := append(os.Environ(), "HOME="+filepath.Join(root, "home"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"))
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
	f := &fx{t: t, repo: repo, g: g, c: &cleanup.Cleaner{Git: g}}
	f.target = f.git(repo.Dir, "symbolic-ref", "--short", "HEAD")
	f.base = f.git(repo.Dir, "rev-parse", "HEAD")
	f.tips = f.commit("feat", f.base, "t.txt")
	f.result = f.commit(workbranch.Name(opID), f.tips, "a.txt")
	f.git(repo.Dir, "merge", "--ff-only", f.result)

	swp := filepath.Join(root, "sw")
	f.git(repo.Dir, "worktree", "add", swp, "feat")
	f.git(swp, "switch", workbranch.Name(opID))
	f.sw = &worktrees.Chosen{
		Path: swp, AdminName: "sw", Roles: []worktrees.Role{worktrees.Source, worktrees.Work},
		Start: "feat", OnBranch: true,
	}
	f.dw = &worktrees.Chosen{
		Path: repo.Dir, AdminName: worktrees.MainAdminName, Roles: []worktrees.Role{worktrees.Destination},
		Start: f.target, IsMain: true,
	}
	return f
}

func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	r := f.g.Run(f.t.Context(), dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v in %s: %s", args, dir, r.Stderr)
	}
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) ref(name string) string {
	r := f.g.Run(f.t.Context(), f.repo.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) write(dir, file, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) commit(branch, from, file string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "scratch-"+strings.ReplaceAll(branch, "/", "-"))
	f.git(f.repo.Dir, "worktree", "add", "-B", branch, p, from)
	f.write(p, file, "x\n")
	f.git(p, "add", file)
	f.git(p, "commit", "-m", "add "+file)
	oid := f.git(p, "rev-parse", "HEAD")
	f.git(f.repo.Dir, "worktree", "remove", "--force", p)
	return oid
}

func (f *fx) input() cleanup.Input {
	return cleanup.Input{
		Operation: worktrees.Land, OpID: opID, Branch: "feat", Target: f.target,
		Tips:      outcome.Tips{Branch: f.tips, Target: f.base, Result: f.result},
		Worktrees: []*worktrees.Chosen{f.sw, f.dw}, Work: f.sw, Dir: f.repo.Dir,
	}
}

func (f *fx) swExists() bool {
	_, err := os.Stat(f.sw.Path)
	return err == nil
}

func (f *fx) swBranch() string {
	r := f.g.Run(f.t.Context(), f.sw.Path, "symbolic-ref", "--short", "-q", "HEAD")
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) placeholder() string { return cleanup.PlaceholderName("sw") }

func (f *fx) run(in cleanup.Input) cleanup.Result {
	f.t.Helper()
	r := f.c.Run(f.t.Context(), in)
	if r.Stop != nil {
		f.t.Fatalf("stopped: %+v", r.Stop)
	}
	return r
}

func warnings(r cleanup.Result) []outcome.Warning {
	var w []outcome.Warning
	for _, x := range r.Warnings {
		w = append(w, x.Word)
	}
	return w
}

func wantWarn(t *testing.T, r cleanup.Result, w outcome.Warning, detailPrefix string) {
	t.Helper()
	for _, x := range r.Warnings {
		if x.Word == w && strings.HasPrefix(x.Detail, detailPrefix) {
			return
		}
	}
	t.Fatalf("warnings = %+v, want %s (%s...)", r.Warnings, w, detailPrefix)
}

func (f *fx) wantWorkBranchGone() {
	f.t.Helper()
	if f.ref(workbranch.Name(opID)) != "" {
		f.t.Fatal("the work branch still exists")
	}
}

func TestKeepFlagTableLand(t *testing.T) {
	cases := []struct {
		name         string
		keepWT, keep bool
		branchFate   string
		wtFate       string
		wantBranch   string // "", "moved" (result) or "tips"
		swState      string // "removed", "placeholder", "branch"
	}{
		{"none", false, false, outcome.BranchDeleted, outcome.WorktreeRemoved, "", "removed"},
		{"keep-worktree", true, false, outcome.BranchDeleted, outcome.WorktreeKept, "", "placeholder"},
		{"keep-branch", false, true, outcome.BranchMoved, outcome.WorktreeRemoved, "result", "removed"},
		{"keep-both", true, true, outcome.BranchMoved, outcome.WorktreeKept, "result", "branch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			in := f.input()
			in.KeepSourceWorktree, in.KeepSourceBranch = tc.keepWT, tc.keep
			r := f.run(in)
			if r.BranchFate != tc.branchFate || r.WorktreeFate.Fate != tc.wtFate {
				t.Fatalf("fates = %s/%+v, want %s/%s", r.BranchFate, r.WorktreeFate, tc.branchFate, tc.wtFate)
			}
			if r.OldBranchTip != f.tips {
				t.Fatalf("old_branch_tip = %s, want %s", r.OldBranchTip, f.tips)
			}
			if len(r.Warnings) != 0 {
				t.Fatalf("warnings = %+v", r.Warnings)
			}
			switch tc.wantBranch {
			case "":
				if f.ref("feat") != "" {
					t.Fatal("feat was not deleted")
				}
			case "result":
				if f.ref("feat") != f.result {
					t.Fatalf("feat = %s, want %s", f.ref("feat"), f.result)
				}
			}
			switch tc.swState {
			case "removed":
				if f.swExists() {
					t.Fatal("the source worktree still exists")
				}
			case "placeholder":
				if got := f.swBranch(); got != f.placeholder() {
					t.Fatalf("sw on %q, want %q", got, f.placeholder())
				}
				if f.ref(f.placeholder()) != f.result {
					t.Fatalf("placeholder = %s, want the landed TARGET %s", f.ref(f.placeholder()), f.result)
				}
			case "branch":
				if got := f.swBranch(); got != "feat" {
					t.Fatalf("sw on %q, want feat", got)
				}
				if st := f.git(f.sw.Path, "status", "--porcelain"); st != "" {
					t.Fatalf("sw not clean: %q", st)
				}
			}
			f.wantWorkBranchGone()
		})
	}
}

func TestNothingToLand(t *testing.T) {
	// BRANCH is contained in TARGET already: tips.branch is the base.
	setup := func(t *testing.T) *fx {
		f := newFx(t)
		f.git(f.repo.Dir, "update-ref", "refs/heads/feat", f.tips)
		return f
	}
	for _, tc := range []struct {
		name         string
		keepWT, keep bool
	}{{"none", false, false}, {"keep-worktree", true, false}, {"keep-branch", false, true}, {"keep-both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			in := f.input()
			in.NothingToLand = true
			in.Tips.Result = ""
			in.KeepSourceWorktree, in.KeepSourceBranch = tc.keepWT, tc.keep
			r := f.run(in)
			if tc.keep {
				if r.BranchFate != outcome.BranchKept || f.ref("feat") != f.tips {
					t.Fatalf("fate %s, feat %s: BRANCH must stay where it is", r.BranchFate, f.ref("feat"))
				}
			} else if r.BranchFate != outcome.BranchDeleted || f.ref("feat") != "" {
				t.Fatalf("fate %s, feat %s", r.BranchFate, f.ref("feat"))
			}
			wantWT := outcome.WorktreeRemoved
			if tc.keepWT {
				wantWT = outcome.WorktreeKept
			}
			if r.WorktreeFate.Fate != wantWT {
				t.Fatalf("worktree fate = %+v", r.WorktreeFate)
			}
			if tc.keepWT && !tc.keep && f.swBranch() != f.placeholder() {
				t.Fatalf("sw on %q", f.swBranch())
			}
			if tc.keepWT && tc.keep && f.swBranch() != "feat" {
				t.Fatalf("sw on %q", f.swBranch())
			}
		})
	}
}

func TestRebaseLeavesSourceOnBranchAtResult(t *testing.T) {
	f := newFx(t)
	in := f.input()
	in.Operation = worktrees.Rebase
	in.Target = ""
	// A rebase has BRANCH at its old tip when cleanup starts only if the
	// branch-move packet did not run; cleanup then moves it.
	r := f.run(in)
	if r.BranchFate != outcome.BranchMoved || r.WorktreeFate.Fate == outcome.WorktreeRemoved {
		t.Fatalf("result = %+v", r)
	}
	if f.ref("feat") != f.result || f.swBranch() != "feat" || !f.swExists() {
		t.Fatalf("feat=%s sw on %q", f.ref("feat"), f.swBranch())
	}
	f.wantWorkBranchGone()

	// BRANCH already at the result: done, nothing written.
	f2 := newFx(t)
	f2.git(f2.repo.Dir, "update-ref", "refs/heads/feat", f2.result)
	in2 := f2.input()
	in2.Operation = worktrees.Rebase
	in2.Target = ""
	r2 := f2.run(in2)
	if r2.BranchFate != outcome.BranchMoved || f2.swBranch() != "feat" {
		t.Fatalf("already-moved: %+v on %q", r2, f2.swBranch())
	}
}

func TestRebaseMoveFailureIsStopCheckoutChanged(t *testing.T) {
	f := newFx(t)
	h := filepath.Join(f.repo.Root(), "holder")
	// SW is not the work worktree here: BRANCH is held in another worktree.
	f.git(f.sw.Path, "switch", "--detach")
	f.git(f.repo.Dir, "worktree", "add", h, "feat")
	f.write(h, "t.txt", "edited\n")
	in := f.input()
	in.Operation = worktrees.Rebase
	in.Target = ""
	r := f.c.Run(t.Context(), in)
	if r.Stop == nil || r.Stop.Reason != outcome.StopCheckoutChanged {
		t.Fatalf("result = %+v", r)
	}
	if f.ref("feat") != f.tips {
		t.Fatal("feat moved")
	}
}

func TestSourceRemovalFailures(t *testing.T) {
	cases := map[string]func(f *fx){
		"untracked file": func(f *fx) { f.write(f.sw.Path, "scratch.txt", "mine\n") },
		"locked":         func(f *fx) { f.git(f.repo.Dir, "worktree", "lock", f.sw.Path) },
		"nested worktree": func(f *fx) {
			f.git(f.repo.Dir, "worktree", "add", "-b", "inner", filepath.Join(f.sw.Path, "inner"), f.base)
		},
	}
	for name, setup := range cases {
		for _, keepBranch := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				f := newFx(t)
				setup(f)
				in := f.input()
				in.KeepSourceBranch = keepBranch
				r := f.run(in)
				if r.WorktreeFate.Fate != outcome.WorktreeRemoveFailed || r.WorktreeFate.Reason == "" {
					t.Fatalf("worktree fate = %+v", r.WorktreeFate)
				}
				wantWarn(t, r, outcome.WarnSourceWorktreeNotRemove, "")
				if !f.swExists() {
					t.Fatal("the worktree was forced away")
				}
				if keepBranch {
					if f.swBranch() != "feat" || f.ref("feat") != f.result {
						t.Fatalf("sw on %q, feat %s: want left on BRANCH at the result", f.swBranch(), f.ref("feat"))
					}
				} else {
					if f.swBranch() != f.placeholder() {
						t.Fatalf("sw on %q, want the placeholder", f.swBranch())
					}
					if f.ref("feat") != "" {
						t.Fatal("feat should be deleted; the outcome stays landed")
					}
				}
			})
		}
	}
}

func TestBranchKeptWhenItMoved(t *testing.T) {
	f := newFx(t)
	f.git(f.repo.Dir, "update-ref", "refs/heads/feat", f.git(f.repo.Dir, "commit-tree", "-p", f.tips, "-m", "extra", f.tips+"^{tree}"))
	moved := f.ref("feat")
	r := f.run(f.input())
	wantWarn(t, r, outcome.WarnBranchNotDeleted, outcome.NotDeletedMoved)
	if r.BranchFate != outcome.BranchKept || f.ref("feat") != moved {
		t.Fatalf("fate %s, feat %s", r.BranchFate, f.ref("feat"))
	}
	if r.WorktreeFate.Fate != outcome.WorktreeRemoved {
		t.Fatalf("worktree fate %+v", r.WorktreeFate)
	}
}

func TestBranchKeptWhenTargetDoesNotContainTheResult(t *testing.T) {
	f := newFx(t)
	f.git(f.repo.Dir, "reset", "--hard", f.base) // TARGET back at the base
	r := f.run(f.input())
	wantWarn(t, r, outcome.WarnBranchNotDeleted, outcome.NotDeletedNotContained)
	if r.BranchFate != outcome.BranchKept || f.ref("feat") != f.tips {
		t.Fatalf("fate %s, feat %s", r.BranchFate, f.ref("feat"))
	}
}

func TestPlaceholderIsRepointedInItsOwnWorktree(t *testing.T) {
	f := newFx(t)
	// A stale placeholder at the base: it is an ancestor of TARGET, so it
	// would be listed by git branch --merged both before and after.
	f.git(f.repo.Dir, "branch", f.placeholder(), f.base)
	in := f.input()
	in.KeepSourceWorktree = true
	f.run(in)
	if f.ref(f.placeholder()) != f.result {
		t.Fatalf("placeholder = %s, want %s", f.ref(f.placeholder()), f.result)
	}
	if f.swBranch() != f.placeholder() {
		t.Fatalf("sw on %q", f.swBranch())
	}
}

func TestPlaceholderNameIsSanitised(t *testing.T) {
	for in, want := range map[string]string{
		"sw":       "pg-git-lander/idle/sw",
		"a b/c":    "pg-git-lander/idle/a-b-c",
		"_main":    "pg-git-lander/idle/_main",
		"..x.lock": "pg-git-lander/idle/x",
		"":         "pg-git-lander/idle/worktree",
	} {
		if got := cleanup.PlaceholderName(in); got != want {
			t.Errorf("PlaceholderName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeepBranchMovesInAnotherCleanWorktree(t *testing.T) {
	f := newFx(t)
	h := filepath.Join(f.repo.Root(), "holder")
	f.git(f.sw.Path, "switch", "--detach")
	f.git(f.repo.Dir, "worktree", "add", h, "feat")
	in := f.input()
	in.KeepSourceBranch = true
	in.KeepSourceWorktree = true
	r := f.run(in)
	if r.BranchFate != outcome.BranchMoved || f.ref("feat") != f.result {
		t.Fatalf("fate %s, feat %s", r.BranchFate, f.ref("feat"))
	}
	if st := f.git(h, "status", "--porcelain"); st != "" {
		t.Fatalf("holder not clean: %q", st)
	}
	if _, err := os.Stat(filepath.Join(h, "a.txt")); err != nil {
		t.Fatal("the holder did not follow the move")
	}
}

func TestKeepBranchDivergenceIsWarningBranchNotMoved(t *testing.T) {
	f := newFx(t)
	h := filepath.Join(f.repo.Root(), "holder")
	f.git(f.sw.Path, "switch", "--detach")
	f.git(f.repo.Dir, "worktree", "add", h, "feat")
	f.write(h, "t.txt", "edited\n")
	in := f.input()
	in.KeepSourceBranch = true
	in.KeepSourceWorktree = true
	r := f.run(in)
	wantWarn(t, r, outcome.WarnBranchNotMoved, "")
	if r.BranchFate != outcome.BranchKept || f.ref("feat") != f.tips {
		t.Fatalf("fate %s, feat %s", r.BranchFate, f.ref("feat"))
	}
}

func TestOtherSwitchedWorktreesAreRestored(t *testing.T) {
	f := newFx(t)
	p := filepath.Join(f.repo.Root(), "dw2")
	f.git(f.repo.Dir, "worktree", "add", "--detach", p, f.base)
	f.git(p, "switch", "-c", "scratchy")
	spare := &worktrees.Chosen{
		Path: p, AdminName: "dw2", Roles: []worktrees.Role{worktrees.Destination},
		Start: f.base, StartDetached: true,
	}
	in := f.input()
	in.Worktrees = append(in.Worktrees, spare)
	r := f.run(in)
	if len(r.Restored) != 1 || r.Restored[0].Worktree != p || r.Restored[0].State != f.base {
		t.Fatalf("restored = %+v", r.Restored)
	}
	if got := f.git(p, "rev-parse", "HEAD"); got != f.base {
		t.Fatalf("spare HEAD = %s", got)
	}
	if r := f.g.Run(t.Context(), p, "symbolic-ref", "-q", "HEAD"); r.OK() {
		t.Fatalf("spare not detached: %s", r.Stdout)
	}
}

func TestSpareSourceWorktreeIsRestoredAndKept(t *testing.T) {
	f := newFx(t)
	p := filepath.Join(f.repo.Root(), "spare")
	f.git(f.repo.Dir, "worktree", "add", "-b", "spare-b", p, f.base)
	f.git(p, "switch", "--detach")
	spare := &worktrees.Chosen{Path: p, AdminName: "spare", Roles: []worktrees.Role{worktrees.Source}, Start: "spare-b"}
	f.git(f.repo.Dir, "branch", "wstart", f.base)
	f.sw.Roles = []worktrees.Role{worktrees.Work}
	f.sw.OnBranch = false
	f.sw.Start = "wstart"
	in := f.input()
	in.Worktrees = []*worktrees.Chosen{f.sw, spare, f.dw}
	r := f.run(in)
	if r.WorktreeFate.Fate != outcome.WorktreeKept || !f.swExists() {
		t.Fatalf("fate %+v", r.WorktreeFate)
	}
	if got := f.git(p, "symbolic-ref", "--short", "HEAD"); got != "spare-b" {
		t.Fatalf("spare on %q", got)
	}
}

func TestEveryActionIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		keepWT, keep bool
	}{{"none", false, false}, {"keep-worktree", true, false}, {"keep-branch", false, true}, {"keep-both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			in := f.input()
			in.KeepSourceWorktree, in.KeepSourceBranch = tc.keepWT, tc.keep
			first := f.run(in)
			second := f.run(in)
			if second.BranchFate != first.BranchFate || second.WorktreeFate.Fate != first.WorktreeFate.Fate {
				t.Fatalf("first %s/%s, second %s/%s", first.BranchFate, first.WorktreeFate.Fate, second.BranchFate, second.WorktreeFate.Fate)
			}
			if len(second.Warnings) != 0 {
				t.Fatalf("warnings on the second pass: %v", warnings(second))
			}
		})
	}
}

func TestResumeAfterPartialProgress(t *testing.T) {
	// BRANCH moved but the switch of the source worktree is still pending.
	f := newFx(t)
	f.git(f.repo.Dir, "update-ref", "refs/heads/feat", f.result)
	in := f.input()
	in.KeepSourceBranch, in.KeepSourceWorktree = true, true
	r := f.run(in)
	if r.BranchFate != outcome.BranchMoved || f.swBranch() != "feat" {
		t.Fatalf("fate %s, sw on %q", r.BranchFate, f.swBranch())
	}
}

func TestWorkingDirectoryMovesToTheRepositoryRootBeforeRemoval(t *testing.T) {
	f := newFx(t)
	t.Chdir(f.sw.Path)
	var gotRoot string
	in := f.input()
	in.Chdir = func(p string) error {
		gotRoot = p
		if f.swExists() == false {
			t.Error("the worktree was removed before the directory changed")
		}
		return os.Chdir(p)
	}
	r := f.run(in)
	if r.WorktreeFate.Fate != outcome.WorktreeRemoved {
		t.Fatalf("fate %+v", r.WorktreeFate)
	}
	if gotRoot != f.repo.Dir {
		t.Fatalf("chdir to %q, want the repository root %q", gotRoot, f.repo.Dir)
	}
	if wd, err := os.Getwd(); err != nil || filepath.Clean(wd) == f.sw.Path {
		t.Fatalf("still in the removed worktree: %q %v", wd, err)
	}
}

func TestLockRecordCheckedInTheDelete(t *testing.T) {
	f := newFx(t)
	p := filepath.Join(f.repo.Root(), "blob")
	f.write(f.repo.Root(), "blob", "rec")
	oid := f.git(f.repo.Dir, "hash-object", "-w", p)
	ref := "refs/pg-git-lander/lock/one"
	f.git(f.repo.Dir, "update-ref", ref, oid)
	in := f.input()
	in.Fence.Locks = []landmove.LockCheck{{Ref: ref, Oid: oid}}
	if r := f.run(in); r.BranchFate != outcome.BranchDeleted {
		t.Fatalf("fate %s", r.BranchFate)
	}

	g := newFx(t)
	p2 := filepath.Join(g.repo.Root(), "blob")
	g.write(g.repo.Root(), "blob", "rec")
	oid2 := g.git(g.repo.Dir, "hash-object", "-w", p2)
	g.git(g.repo.Dir, "update-ref", ref, oid2)
	g.write(g.repo.Root(), "blob", "changed")
	g.git(g.repo.Dir, "update-ref", ref, g.git(g.repo.Dir, "hash-object", "-w", p2))
	in2 := g.input()
	in2.Fence.Locks = []landmove.LockCheck{{Ref: ref, Oid: oid2}}
	r := g.c.Run(t.Context(), in2)
	if r.Stop == nil || r.Stop.Reason != outcome.StopLockLost || g.ref("feat") != g.tips {
		t.Fatalf("result = %+v feat=%s", r, g.ref("feat"))
	}
}

// A holder renews its lease while the clean-up works (removing the worktree
// takes seconds), and each renewal rewrites the lock record. A fence that
// carries the oids read before the clean-up started then fails the verify in
// the BRANCH delete with lock-lost although the lock never changed hands;
// Fence.Current re-reads them at the delete.
func TestRenewalDuringCleanupDoesNotLoseTheLock(t *testing.T) {
	ref := "refs/pg-git-lander/lock/one"
	setup := func() (*fx, string, func() string) {
		f := newFx(t)
		p := filepath.Join(f.repo.Root(), "blob")
		write := func(s string) string {
			f.write(f.repo.Root(), "blob", s)
			oid := f.git(f.repo.Dir, "hash-object", "-w", p)
			f.git(f.repo.Dir, "update-ref", ref, oid)
			return oid
		}
		first := write("renewed at 0")
		return f, first, func() string { return write("renewed at 10") }
	}

	// Reproduction: the oids snapshotted before the renewal are stale.
	f, first, renew := setup()
	in := f.input()
	in.Fence.Locks = []landmove.LockCheck{{Ref: ref, Oid: first}}
	renew()
	r := f.c.Run(t.Context(), in)
	if r.Stop == nil || r.Stop.Reason != outcome.StopLockLost {
		t.Fatalf("a stale snapshot must reproduce lock-lost, got %+v", r)
	}

	// Fix: the same renewal with the oids read at the delete.
	g, first, renew := setup()
	cur := first
	in = g.input()
	in.Fence.Locks = []landmove.LockCheck{{Ref: ref, Oid: first}}
	in.Fence.Current = func() []landmove.LockCheck { return []landmove.LockCheck{{Ref: ref, Oid: cur}} }
	cur = renew()
	if r := g.c.Run(t.Context(), in); r.Stop != nil || r.BranchFate != outcome.BranchDeleted {
		t.Fatalf("result %+v", r)
	}
}
