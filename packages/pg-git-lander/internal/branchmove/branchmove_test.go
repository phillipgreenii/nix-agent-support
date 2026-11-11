package branchmove_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/branchmove"
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
	m    *branchmove.Mover
	g    *gitexec.Git
	// tips is BRANCH's tip (has t.txt); result is the work branch's tip
	// (adds a.txt on top of tips).
	tips, result string
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
	f := &fx{t: t, repo: repo, g: g, m: &branchmove.Mover{Git: g}}
	base := f.git(repo.Dir, "rev-parse", "HEAD")
	f.tips = f.commitOn("feat", base, "t.txt")
	f.result = f.commitOn(workbranch.Name(opID), f.tips, "a.txt")
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

func (f *fx) commitOn(branch, from, file string) string {
	f.t.Helper()
	return f.commitFile(branch, from, file, "x\n")
}

func (f *fx) commitFile(branch, from, file, content string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "scratch-"+strings.ReplaceAll(branch, "/", "-"))
	f.git(f.repo.Dir, "worktree", "add", "-B", branch, p, from)
	f.write(p, file, content)
	f.git(p, "add", file)
	f.git(p, "commit", "-m", "add "+file)
	oid := f.git(p, "rev-parse", "HEAD")
	f.git(f.repo.Dir, "worktree", "remove", "--force", p)
	return oid
}

// workWorktree checks the work branch out in a fresh worktree.
func (f *fx) workWorktree() *worktrees.Chosen {
	p := filepath.Join(f.repo.Root(), "ww")
	f.git(f.repo.Dir, "worktree", "add", p, workbranch.Name(opID))
	return &worktrees.Chosen{Path: p}
}

// holder checks feat out in a fresh worktree.
func (f *fx) holder() *worktrees.Chosen {
	p := filepath.Join(f.repo.Root(), "holder")
	f.git(f.repo.Dir, "worktree", "add", p, "feat")
	return &worktrees.Chosen{Path: p}
}

func (f *fx) input(op worktrees.Operation) branchmove.Input {
	return branchmove.Input{
		Operation: op, Branch: "feat", TipsBranch: f.tips, Result: f.result, Dir: f.repo.Dir,
	}
}

func (f *fx) lock(name, content string) (ref, oid string) {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "blob-"+name)
	f.write(f.repo.Root(), "blob-"+name, content)
	oid = f.git(f.repo.Dir, "hash-object", "-w", p)
	ref = "refs/pg-git-lander/lock/" + name
	f.git(f.repo.Dir, "update-ref", ref, oid)
	return ref, oid
}

func wantStop(t *testing.T, r branchmove.Result, reason outcome.StopReason) {
	t.Helper()
	if r.Status != branchmove.Stopped || r.Stop != reason {
		t.Fatalf("result = %+v, want stop %s", r, reason)
	}
}

func wantNotMoved(t *testing.T, r branchmove.Result) {
	t.Helper()
	if r.Status != branchmove.NotMoved || r.Warning != outcome.WarnBranchNotMoved {
		t.Fatalf("result = %+v, want the warning branch-not-moved", r)
	}
}

func TestNowhereUpdateRefThenSwitchLeavesCleanStatus(t *testing.T) {
	f := newFx(t)
	ww := f.workWorktree()
	lref, loid := f.lock("one", "rec")
	in := f.input(worktrees.Land)
	in.Dir, in.Switch = ww.Path, ww
	in.Fence.Locks = []landmove.LockCheck{{Ref: lref, Oid: loid}}
	for _, op := range []worktrees.Operation{worktrees.Rebase} {
		in.Operation = op
		r := f.m.Move(t.Context(), in)
		if r.Status != branchmove.Moved {
			t.Fatalf("result = %+v", r)
		}
	}
	if f.ref("feat") != f.result {
		t.Fatalf("feat = %s, want %s", f.ref("feat"), f.result)
	}
	if got := f.git(ww.Path, "symbolic-ref", "--short", "HEAD"); got != "feat" {
		t.Fatalf("work worktree is on %q, want feat", got)
	}
	if st := f.git(ww.Path, "status", "--porcelain"); st != "" {
		t.Fatalf("status not clean: %q", st)
	}
}

func TestNowhereLockRecordChangedBlocksTheMove(t *testing.T) {
	f := newFx(t)
	lref, loid := f.lock("one", "rec")
	f.lock("one", "changed")
	in := f.input(worktrees.Land)
	in.Fence.Locks = []landmove.LockCheck{{Ref: lref, Oid: loid}}
	wantStop(t, f.m.Move(t.Context(), in), outcome.StopLockLost)
	if f.ref("feat") != f.tips {
		t.Fatal("feat moved despite the changed lock record")
	}
}

func TestStaleRenewalStopsBeforeAnyWrite(t *testing.T) {
	f := newFx(t)
	in := f.input(worktrees.Land)
	in.Fence.RenewalFresh = func() bool { return false }
	wantStop(t, f.m.Move(t.Context(), in), outcome.StopLockLost)
	if f.ref("feat") != f.tips {
		t.Fatal("feat moved")
	}
}

func TestSourceWorktreeAlreadyRemovedMovesByUpdateRefAlone(t *testing.T) {
	f := newFx(t)
	in := f.input(worktrees.Land) // Switch is nil: nothing to switch
	if r := f.m.Move(t.Context(), in); r.Status != branchmove.Moved {
		t.Fatalf("result = %+v", r)
	}
	if f.ref("feat") != f.result {
		t.Fatalf("feat = %s, want %s", f.ref("feat"), f.result)
	}
}

func TestCheckedOutInCleanOtherWorktreeIsMovedByResetKeepAlone(t *testing.T) {
	for _, op := range []worktrees.Operation{worktrees.Land, worktrees.Rebase} {
		t.Run(string(op), func(t *testing.T) {
			f := newFx(t)
			h := f.holder()
			in := f.input(op)
			in.Holder = h
			if r := f.m.Move(t.Context(), in); r.Status != branchmove.Moved {
				t.Fatalf("result = %+v", r)
			}
			if f.ref("feat") != f.result {
				t.Fatalf("feat = %s, want %s", f.ref("feat"), f.result)
			}
			// update-ref first would leave staged reverse changes.
			if st := f.git(h.Path, "status", "--porcelain"); st != "" {
				t.Fatalf("holder status not clean: %q", st)
			}
			if _, err := os.Stat(filepath.Join(h.Path, "a.txt")); err != nil {
				t.Fatalf("the holder's files did not follow the move: %v", err)
			}
		})
	}
}

func TestHolderRefusalsAreWarningForLandAndStopForRebase(t *testing.T) {
	cases := map[string]func(f *fx, h *worktrees.Chosen){
		"unstaged edit":       func(f *fx, h *worktrees.Chosen) { f.write(h.Path, "t.txt", "edited\n") },
		"colliding untracked": func(f *fx, h *worktrees.Chosen) { f.write(h.Path, "a.txt", "mine\n") },
	}
	for name, dirty := range cases {
		for _, op := range []worktrees.Operation{worktrees.Land, worktrees.Rebase} {
			t.Run(name+"/"+string(op), func(t *testing.T) {
				f := newFx(t)
				h := f.holder()
				dirty(f, h)
				in := f.input(op)
				in.Holder = h
				r := f.m.Move(t.Context(), in)
				if op == worktrees.Land {
					wantNotMoved(t, r)
				} else {
					wantStop(t, r, outcome.StopCheckoutChanged)
				}
				if f.ref("feat") != f.tips {
					t.Fatal("feat moved")
				}
			})
		}
	}
}

func TestResetKeepRefusalOnEditOfAChangedFile(t *testing.T) {
	f := newFx(t)
	// The result changes t.txt, and the holder (tracked change hidden from
	// the status test by git add -N is not possible), so use a staged-clean
	// file the move changes: make result modify t.txt.
	f.result = f.git(f.repo.Dir, "commit-tree", "-p", f.tips, "-m", "chg", f.result+"^{tree}")
	f.git(f.repo.Dir, "update-ref", "-d", "refs/heads/"+workbranch.Name(opID))
	h := f.holder()
	f.write(h.Path, "t.txt", "edited\n")
	in := f.input(worktrees.Rebase)
	in.Holder = h
	wantStop(t, f.m.Move(t.Context(), in), outcome.StopCheckoutChanged)
	if f.ref("feat") != f.tips {
		t.Fatal("feat moved")
	}
}

func TestBranchMovedUnderneath(t *testing.T) {
	for _, hold := range []bool{false, true} {
		name := "nowhere"
		if hold {
			name = "holder"
		}
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			in := f.input(worktrees.Rebase)
			if hold {
				in.Holder = f.holder()
			}
			// a commit added to BRANCH from another worktree
			f.advance()
			wantStop(t, f.m.Move(t.Context(), in), outcome.StopBranchMoved)

			in.Operation = worktrees.Land
			wantNotMoved(t, f.m.Move(t.Context(), in))
		})
	}
}

func TestBranchMovedBetweenCheckAndWriteLosesTheCompareAndSwap(t *testing.T) {
	f := newFx(t)
	in := f.input(worktrees.Rebase)
	f.m.BeforeWrite = func() { f.advance() }
	wantStop(t, f.m.Move(t.Context(), in), outcome.StopBranchMoved)
	if f.ref("feat") == f.result {
		t.Fatal("the compare-and-swap overwrote a branch that moved")
	}
}

// advance adds an empty commit to feat without touching any checkout.
func (f *fx) advance() string {
	f.t.Helper()
	oid := f.git(f.repo.Dir, "commit-tree", "-p", f.tips, "-m", "extra", f.tips+"^{tree}")
	f.git(f.repo.Dir, "update-ref", "refs/heads/feat", oid)
	return oid
}
