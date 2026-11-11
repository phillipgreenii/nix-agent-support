package oprecord

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

func (f *fx) readRef(ref string) locks.ReadState {
	f.t.Helper()
	st, err := f.l.Git, error(nil)
	_ = st
	rs, err := f.l.Read(f.t.Context(), ref)
	if err != nil {
		f.t.Fatal(err)
	}
	return rs
}

// steal replaces the record at ref behind the holder's back, as a waiter's
// conversion or takeover would.
func (f *fx) steal(ref string) {
	f.t.Helper()
	cur := f.readRef(ref)
	rec := cur.Rec.Clone()
	rec.Owner.Token = "thief"
	b, err := rec.MarshalJSON()
	if err != nil {
		f.t.Fatal(err)
	}
	h := f.l.Git.RunInput(f.t.Context(), f.repo.Dir, append(b, '\n'), "hash-object", "-w", "--stdin")
	if !h.OK() {
		f.t.Fatalf("hash-object: %s", h.Stderr)
	}
	if u := f.l.Git.Run(f.t.Context(), f.repo.Dir, "update-ref", ref, strings.TrimSpace(string(h.Stdout)), cur.OID); !u.OK() {
		f.t.Fatalf("update-ref: %s", u.Stderr)
	}
}

func beginLand(t *testing.T, f *fx) *Op {
	t.Helper()
	op, err := Begin(t.Context(), f.l, params(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cc := &locks.CurrentCommand{Argv0: "git", PID: 41790}
	if err := op.SetCurrentCommand(t.Context(), cc); err != nil {
		t.Fatal(err)
	}
	return op
}

func TestLostLockWhenEveryRecordIsStillOurs(t *testing.T) {
	f := newFx(t)
	op := beginLand(t, f)
	var killed []int
	res, err := op.HandleLostLock(t.Context(), LostLock{Kill: func(pid int) { killed = append(killed, pid) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(killed) != 1 || killed[0] != 41790 {
		t.Errorf("killed = %v", killed)
	}
	b := f.readRef(locks.BranchRef("feature/x")).Rec
	if b.State != locks.StateStopped || b.Lease != nil || b.Stop.Reason != "lock-lost" || b.CurrentCommand != nil {
		t.Errorf("branch = %+v stop=%+v", b, b.Stop)
	}
	for _, a := range []string{"feature-x", "_main"} {
		if w := f.readRef(locks.WorktreeRef(a)).Rec; w.State != locks.StateStopped || w.Lease != nil {
			t.Errorf("worktree %s = %+v", a, w)
		}
	}
	if !f.readRef(locks.BranchRef("main")).Absent {
		t.Error("TARGET record not deleted")
	}
	if len(res.NotOwned) != 0 || len(res.Swapped) != 4 {
		t.Errorf("result = %+v", res)
	}
	if o := res.Outcome; o.Exit != 4 || o.Outcome != outcome.Stopped || o.Reason != "lock-lost" || o.Stage != "locked" {
		t.Errorf("outcome = %+v", o)
	}
}

func TestLostLockSwapsOnlyWhatIsStillItsOwn(t *testing.T) {
	f := newFx(t)
	op := beginLand(t, f)
	// another run took the BRANCH record and the TARGET record
	f.steal(locks.BranchRef("feature/x"))
	f.steal(locks.BranchRef("main"))
	thiefBranch := f.readRef(locks.BranchRef("feature/x")).OID
	thiefTarget := f.readRef(locks.BranchRef("main")).OID
	res, err := op.HandleLostLock(t.Context(), LostLock{Kill: func(int) {}})
	if err != nil {
		t.Fatal(err)
	}
	if f.readRef(locks.BranchRef("feature/x")).OID != thiefBranch || f.readRef(locks.BranchRef("main")).OID != thiefTarget {
		t.Error("a record this run no longer owned was written or deleted")
	}
	for _, a := range []string{"feature-x", "_main"} {
		if w := f.readRef(locks.WorktreeRef(a)).Rec; w.State != locks.StateStopped {
			t.Errorf("own worktree record %s not stopped", a)
		}
	}
	want := map[string]bool{locks.BranchRef("feature/x"): true, locks.BranchRef("main"): true}
	if len(res.NotOwned) != 2 || !want[res.NotOwned[0]] || !want[res.NotOwned[1]] {
		t.Errorf("not owned = %v", res.NotOwned)
	}
	if o := res.Outcome; o.Exit != 4 || !strings.Contains(o.Message, locks.BranchRef("feature/x")) || !strings.Contains(o.Message, locks.BranchRef("main")) {
		t.Errorf("outcome must name the records no longer owned: %+v", o)
	}
}

func TestLostLockFromASignalIsInterrupted(t *testing.T) {
	f := newFx(t)
	op := beginLand(t, f)
	f.steal(locks.WorktreeRef("_main"))
	res, err := op.HandleLostLock(t.Context(), LostLock{Reason: outcome.StopInterrupted, Kill: func(int) {}})
	if err != nil {
		t.Fatal(err)
	}
	if b := f.readRef(locks.BranchRef("feature/x")).Rec; b.Stop.Reason != "interrupted" {
		t.Errorf("stop = %+v", b.Stop)
	}
	if len(res.NotOwned) != 1 || res.NotOwned[0] != locks.WorktreeRef("_main") || res.Outcome.Reason != "interrupted" {
		t.Errorf("result = %+v outcome=%+v", res, res.Outcome)
	}
}
