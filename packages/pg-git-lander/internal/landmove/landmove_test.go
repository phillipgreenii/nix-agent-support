package landmove_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/landmove"
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
	m    *landmove.Mover
	base string
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
	f := &fx{
		t: t, repo: repo, g: g,
		m: &landmove.Mover{Git: g, Brancher: &workbranch.Brancher{Git: g}},
	}
	f.base = f.git(repo.Dir, "rev-parse", "HEAD")
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

func (f *fx) cur(dir string) string {
	r := f.g.Run(f.t.Context(), dir, "symbolic-ref", "--short", "-q", "HEAD")
	return strings.TrimSpace(string(r.Stdout))
}

// commitOn creates branch (at from) in a scratch worktree, commits file and
// returns the new commit; the scratch worktree is removed.
func (f *fx) commitOn(branch, from, file string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "scratch-"+branch)
	f.git(f.repo.Dir, "worktree", "add", "-B", branch, p, from)
	if err := os.WriteFile(filepath.Join(p, file), []byte("x\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(p, "add", file)
	f.git(p, "commit", "-m", "add "+file)
	oid := f.git(p, "rev-parse", "HEAD")
	f.git(f.repo.Dir, "worktree", "remove", "--force", p)
	return oid
}

func (f *fx) chosen(dir string) *worktrees.Chosen {
	return &worktrees.Chosen{Path: dir, Start: f.cur(dir)}
}

// workOnBranch makes the work branch at result and checks it out in a fresh
// work worktree.
func (f *fx) workWorktree(result string) *worktrees.Chosen {
	p := filepath.Join(f.repo.Root(), "ww")
	f.git(f.repo.Dir, "worktree", "add", "-b", workbranch.Name(opID), p, result)
	return &worktrees.Chosen{Path: p, Start: "main"}
}

func (f *fx) lock(name, content string) (ref, oid string) {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "blob-"+name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	oid = f.git(f.repo.Dir, "hash-object", "-w", p)
	ref = "refs/pg-git-lander/lock/" + name
	f.git(f.repo.Dir, "update-ref", ref, oid)
	return ref, oid
}

func (f *fx) hook(name, script string) {
	f.t.Helper()
	dir := f.git(f.repo.Dir, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func wantStop(t *testing.T, r landmove.Result, reason outcome.StopReason) {
	t.Helper()
	if r.Status != landmove.Stopped || r.Stop != reason {
		t.Fatalf("result = %+v, want stop %s", r, reason)
	}
}

func TestNowhereMovedByUpdateRefWithLocksVerifiedAndIntentFirst(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.git(f.repo.Dir, "branch", "tgt", f.base)
	lref, loid := f.lock("one", "rec")
	var seen string
	in := landmove.Input{
		OpID: opID, Target: "tgt", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: false,
		Fence: landmove.Fence{Locks: []landmove.LockCheck{{Ref: lref, Oid: loid}}},
		Intent: func(exp string) error {
			seen = exp
			if f.ref("tgt") != f.base {
				t.Error("TARGET already moved when the intent was reported")
			}
			return nil
		},
	}
	r := f.m.Move(t.Context(), in)
	if r.Status != landmove.Moved || f.ref("tgt") != result || seen != result {
		t.Fatalf("result %+v tgt=%s seen=%s", r, f.ref("tgt"), seen)
	}
	if f.cur(f.repo.Dir) != "main" {
		t.Fatal("a worktree was switched")
	}
}

func TestNowhereChangedLockRecordIsLockLostNothingMoved(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.git(f.repo.Dir, "branch", "tgt", f.base)
	lref, _ := f.lock("one", "rec")
	_, other := f.lock("two", "changed")
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "tgt", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), Fence: landmove.Fence{Locks: []landmove.LockCheck{{Ref: lref, Oid: other}}},
	})
	wantStop(t, r, outcome.StopLockLost)
	if f.ref("tgt") != f.base {
		t.Fatal("TARGET moved")
	}
}

func TestStaleRenewalIsLockLostNothingMoved(t *testing.T) {
	for _, nowhere := range []bool{true, false} {
		f := newFx(t)
		result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
		tgt := "tgt"
		if !nowhere {
			tgt = "main"
		}
		f.git(f.repo.Dir, "branch", "-f", "tgt", f.base)
		r := f.m.Move(t.Context(), landmove.Input{
			OpID: opID, Target: tgt, TipsTarget: f.base, Result: result,
			DW: f.chosen(f.repo.Dir), TargetCheckedOut: !nowhere,
			Fence: landmove.Fence{RenewalFresh: func() bool { return false }},
		})
		wantStop(t, r, outcome.StopLockLost)
		if f.ref(tgt) != f.base {
			t.Fatalf("nowhere=%v: TARGET moved", nowhere)
		}
	}
}

func TestCheckedOutDestinationMovedByMergeFFOnly(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	lref, loid := f.lock("one", "rec")
	var seen string
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
		Fence:  landmove.Fence{Locks: []landmove.LockCheck{{Ref: lref, Oid: loid}}},
		Intent: func(exp string) error { seen = exp; return nil },
	})
	if r.Status != landmove.Moved || f.ref("main") != result || seen != result {
		t.Fatalf("result %+v main=%s", r, f.ref("main"))
	}
	if _, err := os.Stat(filepath.Join(f.repo.Dir, "a.txt")); err != nil {
		t.Fatal("DW tree not advanced")
	}
}

func TestCheckedOutChangedLockRecordIsLockLost(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	lref, _ := f.lock("one", "rec")
	_, other := f.lock("two", "changed")
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
		Fence: landmove.Fence{Locks: []landmove.LockCheck{{Ref: lref, Oid: other}}},
	})
	wantStop(t, r, outcome.StopLockLost)
	if f.ref("main") != f.base {
		t.Fatal("TARGET moved")
	}
}

func TestDestinationEqualsWorkWorktreeSwitchedAwayAndLeftOnTarget(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.git(f.repo.Dir, "switch", workbranch.Name(opID))
	dw := &worktrees.Chosen{Path: f.repo.Dir, Start: "main"}
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: dw, WW: dw, TargetCheckedOut: true,
	})
	if r.Status != landmove.Moved || f.ref("main") != result || f.cur(f.repo.Dir) != "main" {
		t.Fatalf("result %+v main=%s cur=%s", r, f.ref("main"), f.cur(f.repo.Dir))
	}
}

func TestDestinationEqualsWorkWorktreeBackOnWorkBranchAtStop(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.git(f.repo.Dir, "switch", workbranch.Name(opID))
	dw := &worktrees.Chosen{Path: f.repo.Dir, Start: "main"}
	f.m.BeforeWrite = func() { // after the switch to TARGET: a hook refuses the write
		f.hook("reference-transaction", "cat | grep -q ' refs/heads/main$' && [ \"$1\" = prepared ] && exit 1\nexit 0")
	}
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: dw, WW: dw, TargetCheckedOut: true,
	})
	wantStop(t, r, outcome.StopGitError)
	if f.cur(f.repo.Dir) != workbranch.Name(opID) {
		t.Fatalf("on %q, want the work branch", f.cur(f.repo.Dir))
	}
	if f.ref("main") != f.base {
		t.Fatal("TARGET moved")
	}
}

func TestNowhereTargetMovedBetweenProofAndWriteIsTargetMoved(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.git(f.repo.Dir, "branch", "tgt", f.base)
	second := f.commitOn("other", f.base, "b.txt")
	f.m.BeforeWrite = func() { f.git(f.repo.Dir, "update-ref", "refs/heads/tgt", second) }
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "tgt", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir),
	})
	if r.Status != landmove.TargetMoved || f.ref("tgt") != second {
		t.Fatalf("result %+v tgt=%s", r, f.ref("tgt"))
	}
}

func TestCheckedOutTargetMovedBeforeMergeIsTargetMoved(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	second := f.commitOn("other", f.base, "b.txt")
	f.m.BeforeWrite = func() { f.git(f.repo.Dir, "update-ref", "refs/heads/main", second) }
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
	})
	if r.Status != landmove.TargetMoved {
		t.Fatalf("result %+v", r)
	}
}

func TestResultNotDescendingFromUnmovedTargetIsRacePath(t *testing.T) {
	f := newFx(t)
	second := f.commitOn("other", f.base, "b.txt")
	f.git(f.repo.Dir, "switch", "-C", "tgt", second)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "tgt", TipsTarget: second, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
	})
	if r.Status != landmove.TargetMoved || f.ref("tgt") != second {
		t.Fatalf("result %+v", r)
	}
}

func TestMergeFFOnlyFailureWithTargetUnchangedIsGitError(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	if err := os.WriteFile(filepath.Join(f.repo.Dir, "a.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
	})
	wantStop(t, r, outcome.StopGitError)
	if f.ref("main") != f.base {
		t.Fatal("TARGET moved")
	}
}

func TestDestinationOnOtherBranchOrDirtyIsCheckoutChanged(t *testing.T) {
	f := newFx(t)
	if err := os.WriteFile(filepath.Join(f.repo.Dir, "tracked.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo.Dir, "add", "tracked.txt")
	f.git(f.repo.Dir, "commit", "-m", "tracked")
	f.base = f.git(f.repo.Dir, "rev-parse", "HEAD")
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.git(f.repo.Dir, "switch", "-c", "elsewhere")
	in := landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
	}
	wantStop(t, f.m.Move(t.Context(), in), outcome.StopCheckoutChanged)
	f.git(f.repo.Dir, "switch", "main")
	// a tracked change in the destination
	if err := os.WriteFile(filepath.Join(f.repo.Dir, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantStop(t, f.m.Move(t.Context(), in), outcome.StopCheckoutChanged)
	if f.ref("main") != f.base {
		t.Fatal("TARGET moved")
	}
}

func TestTargetNotTheResultAfterMoveIsMoveUnverified(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	f.hook("post-merge", "git update-ref refs/heads/main "+f.base)
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
	})
	wantStop(t, r, outcome.StopMoveUnverified)
}

func TestIntentErrorStopsBeforeAnyMove(t *testing.T) {
	f := newFx(t)
	result := f.commitOn("pg-git-lander/"+opID, f.base, "a.txt")
	r := f.m.Move(t.Context(), landmove.Input{
		OpID: opID, Target: "main", TipsTarget: f.base, Result: result,
		DW: f.chosen(f.repo.Dir), TargetCheckedOut: true,
		Intent: func(string) error { return os.ErrPermission },
	})
	wantStop(t, r, outcome.StopInternalError)
	if f.ref("main") != f.base {
		t.Fatal("TARGET moved")
	}
}
