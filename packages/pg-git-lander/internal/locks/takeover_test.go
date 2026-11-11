package locks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
)

const (
	thisHost    = "laptop.local"
	otherHost   = "other.example"
	startToken  = "1794132898"
	deadPID     = 41721
	deadOpID    = "dead-op"
	waiterOpID  = "waiter-op"
	landTarget  = "main"
	landBranch  = "feature/x"
	otherBranch = "feature/y"
)

// withLiveness gives the fixture's layer a liveness fake and this host's name,
// and a wait shorter than the lease so only the dead-pid rule can act.
func (f *fx) withLiveness() (*seamstest.Liveness, *[]Conversion) {
	lv := seamstest.NewLiveness()
	f.l.Liveness = lv
	f.l.Host = seamstest.Host(thisHost)
	var got []Conversion
	f.l.OnConvert = func(_ context.Context, c Conversion) { got = append(got, c) }
	return lv, &got
}

func (f *fx) running(ref string, rec Record) string {
	f.t.Helper()
	rec.Lease = &Lease{RenewedAt: time.Now(), TTLSec: 30}
	return f.plant(ref, rec)
}

// plantLand plants a land operation: BRANCH, TARGET, and worktree records.
func (f *fx) plantLand(host string) (branch, target, wtA, wtB string) {
	f.t.Helper()
	rec := branchRec(landBranch, landTarget, deadOpID, "feature-x", "_main")
	rec.Owner.Host = host
	branch = f.running(BranchRef(landBranch), rec)
	target = f.running(BranchRef(landTarget), rec.Short(RoleTarget))
	wtA = f.running(WorktreeRef("feature-x"), rec.Short(RoleWorktree))
	wtB = f.running(WorktreeRef("_main"), rec.Short(RoleWorktree))
	return
}

func waiterSpec(ref string, role Role) Spec {
	r := branchRec(otherBranch, landTarget, waiterOpID)
	r.Owner.Token = "waiter"
	r.Owner.PID = 7
	if role != RoleBranch {
		r = r.Short(role)
	}
	return Spec{Ref: ref, Role: role, Rec: r}
}

func (f *fx) mustRead(ref string) ReadState {
	f.t.Helper()
	st, err := f.l.Read(f.t.Context(), ref)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func TestDeadSameHostPidIsConvertedAtOnce(t *testing.T) {
	cases := map[string]func(lv *seamstest.Liveness){
		"gone":   func(lv *seamstest.Liveness) {},
		"reused": func(lv *seamstest.Liveness) { lv.SetAlive(deadPID, "another-start") },
		"zombie": func(lv *seamstest.Liveness) { lv.SetZombie(deadPID, startToken) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			lv, conv := f.withLiveness()
			setup(lv)
			f.plantLand(thisHost)
			_, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landBranch), RoleBranch))
			var rf *RefusedError
			if !errors.As(err, &rf) || rf.Reason != outcome.RefHolderDied {
				t.Fatalf("err = %v", err)
			}
			if len(f.sleeps) != 0 {
				t.Errorf("a dead holder must be converted at once, slept %v", f.sleeps)
			}
			if st := f.mustRead(BranchRef(landBranch)); st.Rec.State != StateStopped || st.Rec.Stop.Reason != "holder-died" {
				t.Errorf("branch = %+v", st.Rec)
			}
			if len(*conv) != 1 || (*conv)[0].Why != WhyDead || (*conv)[0].Lock != BranchRef(landBranch) || (*conv)[0].PreviousOwner.PID != deadPID {
				t.Errorf("conversions = %+v", *conv)
			}
		})
	}
}

func TestLiveSameHostPidIsNotConverted(t *testing.T) {
	f := newFx(t)
	lv, conv := f.withLiveness()
	lv.SetAlive(deadPID, startToken)
	f.l.Config.Wait = 5 * time.Second // shorter than the lease
	oid, _, _, _ := f.plantLand(thisHost)
	_, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landBranch), RoleBranch))
	var be *BusyError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v", err)
	}
	if f.refOID(BranchRef(landBranch)) != oid || len(*conv) != 0 {
		t.Error("a live holder's record was touched")
	}
}

func TestForeignHostOnlyExpiresByObservation(t *testing.T) {
	f := newFx(t)
	lv, conv := f.withLiveness() // pid is "gone" here, but that is another host's pid
	_ = lv
	f.plantLand(otherHost)
	f.l.Config.Wait = 20 * time.Second
	_, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landBranch), RoleBranch))
	var be *BusyError
	if !errors.As(err, &be) {
		t.Fatalf("a foreign pid must not be probed: %v", err)
	}
	if len(*conv) != 0 {
		t.Fatal("converted before the lease expired")
	}
	f.l.Config.Wait = 60 * time.Second
	start := f.clock.Now()
	_, err = f.l.Acquire(t.Context(), waiterSpec(BranchRef(landBranch), RoleBranch))
	var rf *RefusedError
	if !errors.As(err, &rf) || rf.Reason != outcome.RefHolderDied {
		t.Fatalf("err = %v", err)
	}
	if len(*conv) != 1 || (*conv)[0].Why != WhyExpired {
		t.Fatalf("conversions = %+v", *conv)
	}
	// the record was first seen during the first Acquire, so it expired
	// within lease_ttl of this run's second start at the latest
	if el := f.clock.Now().Sub(start); el > f.l.Config.LeaseTTL {
		t.Errorf("expired after %v", el)
	}
}

func TestChangedRecordRestartsTheObservation(t *testing.T) {
	f := newFx(t)
	f.withLiveness()
	rec := branchRec(landBranch, landTarget, deadOpID, "feature-x")
	rec.Owner.Host = otherHost
	oid := f.running(BranchRef(landBranch), rec)
	st := f.mustRead(BranchRef(landBranch))
	if f.l.Classify(st) != FreshnessForeignHost {
		t.Fatalf("class = %v", f.l.Classify(st))
	}
	if why := f.l.Judge(st); why != "" {
		t.Fatalf("expired at first sight: %v", why)
	}
	f.clock.Advance(29 * time.Second)
	// the holder renews: a new oid, so the watch starts over
	rec.Lease = &Lease{RenewedAt: time.Now().Add(time.Hour), TTLSec: 30}
	f.plant(BranchRef(landBranch), rec)
	st2 := f.mustRead(BranchRef(landBranch))
	if st2.OID == oid {
		t.Fatal("renewal did not change the oid")
	}
	f.l.Judge(st2)
	f.clock.Advance(29 * time.Second)
	if why := f.l.Judge(st2); why != "" {
		t.Errorf("58 s after first sight but only 29 s on this oid: %v", why)
	}
	f.clock.Advance(time.Second)
	if why := f.l.Judge(st2); why != WhyExpired {
		t.Errorf("why = %q", why)
	}
}

// stepClock is a clock whose wall time can jump while its monotonic reading
// does not.
type stepClock struct {
	base time.Time
	mono time.Duration
	step time.Duration
	at   map[int64]time.Duration
}

func newStepClock() *stepClock {
	return &stepClock{base: time.Now(), at: map[int64]time.Duration{}}
}

func (c *stepClock) Now() time.Time {
	t := c.base.Add(c.mono + c.step)
	c.at[t.UnixNano()] = c.mono
	return t
}

func (c *stepClock) Since(t time.Time) time.Duration { return c.mono - c.at[t.UnixNano()] }

func TestWallClockStepNeverExpiresALiveLease(t *testing.T) {
	f := newFx(t)
	sc := newStepClock()
	f.l.Clock = sc
	f.withLiveness()
	rec := branchRec(landBranch, landTarget, deadOpID, "feature-x")
	rec.Owner.Host = otherHost
	f.running(BranchRef(landBranch), rec)
	st := f.mustRead(BranchRef(landBranch))
	f.l.Judge(st)
	sc.step = 6 * time.Hour // wall clock jumps (host slept, NTP step)
	sc.mono += 10 * time.Second
	if why := f.l.Judge(st); why != "" {
		t.Fatalf("a wall-clock step expired the lease: %v", why)
	}
	sc.step = -6 * time.Hour
	sc.mono += 19 * time.Second
	if why := f.l.Judge(st); why != "" {
		t.Fatalf("29 s of monotonic time expired the lease: %v", why)
	}
	sc.mono += time.Second
	if why := f.l.Judge(st); why != WhyExpired {
		t.Fatalf("30 s of monotonic time did not expire it: %q", why)
	}
}

func TestClassifyFreshnessClasses(t *testing.T) {
	f := newFx(t)
	lv, _ := f.withLiveness()
	lv.SetAlive(deadPID, startToken)
	mk := func(ref, host string, pid int) ReadState {
		rec := branchRec("b-"+ref[len(ref)-1:], "", "op")
		rec.Owner.Host, rec.Owner.PID = host, pid
		f.running(BranchRef("b-"+ref), rec)
		return f.mustRead(BranchRef("b-" + ref))
	}
	live := mk("1", thisHost, deadPID)
	if got := f.l.Classify(live); got != FreshnessUnobserved {
		t.Errorf("unwatched = %q", got)
	}
	f.l.Judge(live)
	if got := f.l.Classify(live); got != FreshnessFresh {
		t.Errorf("watched = %q", got)
	}
	if got := f.l.Classify(mk("2", thisHost, 99)); got != FreshnessDead {
		t.Errorf("dead = %q", got)
	}
	if got := f.l.Classify(mk("3", otherHost, 99)); got != FreshnessForeignHost {
		t.Errorf("foreign = %q", got)
	}
	stopped := branchRec("b-4", "", "op")
	stopped.State, stopped.Lease, stopped.Stop = StateStopped, nil, &Stop{Reason: "conflict"}
	f.plant(BranchRef("b-4"), stopped)
	if got := f.l.Classify(f.mustRead(BranchRef("b-4"))); got != "" {
		t.Errorf("stopped = %q", got)
	}
}

func TestDeadTargetRecordIsTakenOverByCompareAndSwap(t *testing.T) {
	f := newFx(t)
	_, conv := f.withLiveness()
	_, target, _, _ := f.plantLand(thisHost)
	set, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landTarget), RoleTarget))
	if err != nil {
		t.Fatal(err)
	}
	st := f.mustRead(BranchRef(landTarget))
	if st.Rec.Role != RoleTarget || st.Rec.OpID != waiterOpID || st.Rec.Owner.Token != "waiter" || set.Locks[0].OID() != st.OID {
		t.Errorf("target = %+v", st.Rec)
	}
	// compare-and-swap against the stale record the waiter read, not a blind write
	found := false
	for _, c := range f.rec.matching("update-ref", BranchRef(landTarget)) {
		if len(c) == 4 && c[3] == target {
			found = true
		}
	}
	if !found {
		t.Errorf("no update-ref with the stale oid %s: %v", target, f.rec.matching("update-ref"))
	}
	if len(*conv) != 1 || !(*conv)[0].Takeover || (*conv)[0].Why != WhyDead || (*conv)[0].Lock != BranchRef(landTarget) {
		t.Errorf("conversions = %+v", *conv)
	}
	// the dead operation's BRANCH record is untouched: only TARGET was taken over
	if b := f.mustRead(BranchRef(landBranch)); b.Rec.State != StateRunning {
		t.Error("branch record converted by a target takeover")
	}
}

func TestTargetTakeoverLosingTheRaceReReads(t *testing.T) {
	f := newFx(t)
	f.withLiveness()
	f.plantLand(thisHost)
	// a second waiter wins the takeover first: ours must re-read and then
	// find a live (fresh) record
	won := branchRec(otherBranch, landTarget, "winner").Short(RoleTarget)
	won.Owner.Host, won.Owner.PID = thisHost, 5
	lv := f.l.Liveness.(*seamstest.Liveness)
	lv.SetAlive(5, startToken)
	won.Owner.PIDStart = 1794132898
	st := f.mustRead(BranchRef(landTarget))
	f.running(BranchRef(landTarget), won)
	_ = st
	f.l.Config.Wait = 3 * time.Second
	_, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landTarget), RoleTarget))
	var be *BusyError
	if !errors.As(err, &be) || be.Holder.OpID != "winner" {
		t.Fatalf("err = %v", err)
	}
}

func TestDeadBranchRecordIsConvertedInOneTransaction(t *testing.T) {
	f := newFx(t)
	_, conv := f.withLiveness()
	f.plantLand(thisHost)
	before := len(f.rec.matching("update-ref", "--stdin"))
	_, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landBranch), RoleBranch))
	var rf *RefusedError
	if !errors.As(err, &rf) || rf.Reason != outcome.RefHolderDied || rf.Holder.OpID != deadOpID {
		t.Fatalf("err = %v", err)
	}
	if o := rf.Outcome("rebase", otherBranch, ""); o.Exit != 5 || o.Reason != "holder-died" || len(o.Next) != 2 {
		t.Errorf("outcome = %+v", o)
	}
	if n := len(f.rec.matching("update-ref", "--stdin")) - before; n != 1 {
		t.Errorf("transactions = %d, want exactly 1", n)
	}
	b := f.mustRead(BranchRef(landBranch)).Rec
	if b.State != StateStopped || b.Lease != nil || b.Stop == nil || b.Stop.Reason != "holder-died" || !b.Stop.Retryable || b.Stage != "rebasing" || b.Stop.Stage != "rebasing" {
		t.Errorf("branch = %+v stop=%+v", b, b.Stop)
	}
	if b.Owner.PID != deadPID || b.OpID != deadOpID {
		t.Error("the stopped copy must keep the dead owner and operation")
	}
	for _, a := range []string{"feature-x", "_main"} {
		w := f.mustRead(WorktreeRef(a)).Rec
		if w.State != StateStopped || w.Lease != nil || w.OpID != deadOpID {
			t.Errorf("worktree %s = %+v", a, w)
		}
	}
	if st := f.mustRead(BranchRef(landTarget)); !st.Absent {
		t.Error("TARGET record still held by the dead owner must be deleted")
	}
	if len(*conv) != 1 || (*conv)[0].Takeover || len((*conv)[0].Refs) != 4 {
		t.Errorf("conversions = %+v", *conv)
	}
	// the waiter's own locks are not left behind
	if st := f.mustRead(BranchRef(otherBranch)); !st.Absent {
		t.Error("waiter lock left held")
	}
}

func TestConversionKeepsATargetRecordOfAnotherOwner(t *testing.T) {
	f := newFx(t)
	f.withLiveness()
	f.plantLand(thisHost)
	// TARGET was meanwhile taken over by someone else (a live operation)
	other := branchRec(landBranch, landTarget, deadOpID).Short(RoleTarget)
	other.Owner.Token, other.Owner.PID = "else", 99
	oid := f.running(BranchRef(landTarget), other)
	_, err := f.l.Acquire(t.Context(), waiterSpec(BranchRef(landBranch), RoleBranch))
	var rf *RefusedError
	if !errors.As(err, &rf) || rf.Reason != outcome.RefHolderDied {
		t.Fatalf("err = %v", err)
	}
	if f.refOID(BranchRef(landTarget)) != oid {
		t.Error("TARGET record of another owner was deleted")
	}
}

func TestConversionCanStartFromAWorktreeRecord(t *testing.T) {
	f := newFx(t)
	_, conv := f.withLiveness()
	f.plantLand(thisHost)
	_, err := f.l.Acquire(t.Context(), waiterSpec(WorktreeRef("_main"), RoleWorktree))
	var rf *RefusedError
	if !errors.As(err, &rf) || rf.Reason != outcome.RefHolderDied {
		t.Fatalf("err = %v", err)
	}
	if b := f.mustRead(BranchRef(landBranch)).Rec; b.State != StateStopped || b.Stop.Reason != "holder-died" {
		t.Errorf("the BRANCH record the worktree record names was not converted: %+v", b)
	}
	if w := f.mustRead(WorktreeRef("feature-x")).Rec; w.State != StateStopped {
		t.Error("sibling worktree record not converted")
	}
	if len(*conv) != 1 || (*conv)[0].Lock != WorktreeRef("_main") {
		t.Errorf("conversions = %+v", *conv)
	}
}

func TestWorktreeRecordWithoutItsBranchRecordIsNeverTakenOverAlone(t *testing.T) {
	f := newFx(t)
	_, conv := f.withLiveness()
	f.l.Config.Wait = 3 * time.Second
	rec := branchRec(landBranch, landTarget, deadOpID, "feature-x")
	oid := f.running(WorktreeRef("feature-x"), rec.Short(RoleWorktree))
	_, err := f.l.Acquire(t.Context(), waiterSpec(WorktreeRef("feature-x"), RoleWorktree))
	var be *BusyError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v", err)
	}
	if f.refOID(WorktreeRef("feature-x")) != oid || len(*conv) != 0 {
		t.Error("worktree record converted alone")
	}
}

func TestUnreadableRecordIsNeverJudgedDead(t *testing.T) {
	f := newFx(t)
	f.withLiveness() // pid unknown to the fake: would be dead if it parsed
	ref := BranchRef(landBranch)
	oid := f.plantRaw(ref, []byte(`{"schema":2,"role":"branch","state":"running"}`))
	_, err := f.l.Acquire(t.Context(), waiterSpec(ref, RoleBranch))
	var rf *RefusedError
	if !errors.As(err, &rf) || rf.Reason != outcome.RefUnreadableLock || f.refOID(ref) != oid {
		t.Fatalf("err = %v", err)
	}
}
