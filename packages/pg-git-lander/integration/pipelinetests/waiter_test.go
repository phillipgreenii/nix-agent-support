//go:build integration

package pipelinetests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Tests of a waiter that finds the main worktree lock held by a STOPPED
// operation (design section 8.9). The stopped operation is a real land of
// feature/x that stopped, then had its record forged to the stop under test
// and a stopped destination-worktree record added; the waiter lands feature/y
// (and feature/z) into the same main worktree.

type holdSpec struct {
	stage     string
	reason    string
	retryable bool
	moved     bool
	age       time.Duration
	host      string // owner host; "" is this host
}

// stoppedHoldingMain leaves a stopped land of feature/x that holds the
// destination (main) worktree lock.
func (f *fx) stoppedHoldingMain(h holdSpec) (result string) {
	f.t.Helper()
	f.feature()
	marker := filepath.Join(f.t.TempDir(), "go")
	f.failingBeforeMove(marker)
	f.wantStopped(f.run(f.landWithWork()), outcome.StopHookFailed)
	if err := os.WriteFile(marker, nil, 0o644); err != nil { // later hooks pass
		f.t.Fatal(err)
	}
	result = f.git(f.repo.Dir, "rev-parse", "refs/heads/"+f.workBranch())
	if h.moved {
		f.git(f.repo.Dir, "merge", "-q", "--ff-only", result)
	}
	f.forge(func(r map[string]any) {
		r["stage"] = h.stage
		if h.moved {
			r["move"] = map[string]any{"intent": "moving", "expected_result": result}
		}
		if h.host != "" {
			r["owner"].(map[string]any)["host"] = h.host
		}
		r["stop"] = map[string]any{
			"reason": h.reason, "retryable": h.retryable, "stage": h.stage, "paths": []string{},
			"message": "forged", "moved_already": h.moved,
			"at": f.clock.Now().Add(-h.age).UTC().Format("2006-01-02T15:04:05Z"),
		}
	})
	br := f.record(locks.BranchRef("feature/x"))
	short := map[string]any{
		"schema": 1, "role": "worktree", "state": "stopped", "op_id": br["op_id"], "branch": "feature/x",
		"owner": br["owner"], "lease": nil,
	}
	b, _ := json.Marshal(short)
	hh := f.g.RunInput(f.t.Context(), f.repo.Dir, append(b, '\n'), "hash-object", "-w", "--stdin")
	f.git(f.repo.Dir, "update-ref", locks.WorktreeRef("_main"), strings.TrimSpace(string(hh.Stdout)))
	return result
}

func (f *fx) workBranch() string {
	f.t.Helper()
	return strings.TrimSpace(f.git(f.repo.Dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/pg-git-lander/"))
}

// branchWorktree adds a worktree with one commit of its own on a new branch.
func (f *fx) branchWorktree(branch string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "wt-"+strings.TrimPrefix(branch, "feature/"))
	f.git(f.repo.Dir, "worktree", "add", "-q", "-b", branch, p)
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		f.t.Fatal(err)
	}
	f.commit(r, strings.TrimPrefix(branch, "feature/")+".txt", branch+"\n")
	return r
}

func (f *fx) landOf(branch, _ string) pipeline.Request {
	return pipeline.Request{Operation: worktrees.Land, Branch: branch, Target: f.main, Dir: f.repo.Dir}
}

// waiterRun starts and runs a land with deps d. The test fails when Start does
// not return a prepared operation.
func (f *fx) waiterRun(d pipeline.Deps, req pipeline.Request) *outcome.Outcome {
	f.t.Helper()
	res, err := pipeline.Start(f.t.Context(), d, req)
	if err != nil || res.Prepared == nil {
		f.t.Fatalf("Start: %v %+v %+v", err, res, res.Outcome)
	}
	f.t.Cleanup(func() { _ = res.Prepared.Release(context.Background()); _ = res.Prepared.Close() })
	out, err := res.Prepared.Run(f.t.Context())
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return out
}

// countingDeps is a waiter's deps whose sleeps are counted and, optionally,
// observed (n is the number of sleeps so far).
func (f *fx) countingDeps(o locks.Owner, onSleep func(n int)) (pipeline.Deps, *int, *bytes.Buffer) {
	d := f.deps(o)
	n := 0
	out := &bytes.Buffer{}
	d.Out = out
	d.Sleep = func(_ context.Context, dur time.Duration) error {
		f.clock.Advance(dur)
		n++
		if onSleep != nil {
			onSleep(n)
		}
		return nil
	}
	return d, &n, out
}

// wantTargetBusy asserts the waiter's own land stopped retryably with
// target-busy (it never held main).
func (f *fx) wantTargetBusy(o *outcome.Outcome) {
	f.t.Helper()
	want(f.t, o, outcome.Stopped, 21)
	if o.Reason != string(outcome.StopTargetBusy) || o.Retryable == nil || !*o.Retryable {
		f.t.Fatalf("reason %q retryable %v (%s)", o.Reason, o.Retryable, o.Message)
	}
}

// wantSettle asserts the events file carries a settle event of action about
// the stopped feature/x, with its op id, stage, reason and holder state.
func (f *fx) wantSettle(action, stage string) {
	f.t.Helper()
	for _, e := range f.events() {
		if e["event"] == "settle" && e["action"] == action {
			if e["stopped_branch"] != "feature/x" || e["stopped_op_id"] == "" || e["stage"] != stage ||
				e["stop_reason"] == "" || e["holder_state"] != "stopped" || e["branch"] != "feature/y" {
				f.t.Errorf("settle %s = %v", action, e)
			}
			return
		}
	}
	f.t.Fatalf("no settle event with action %s in %v", action, f.events())
}

func (f *fx) wantLanded(branch string) {
	f.t.Helper()
	if !strings.Contains(f.git(f.repo.Dir, "log", "--format=%s", f.main), "add "+strings.TrimPrefix(branch, "feature/")+".txt") {
		f.t.Fatalf("%s is not on %s", branch, f.main)
	}
}

func TestWaiterWaitsForAYoungRetryableStopAndLandsAfterTheOwnerResumes(t *testing.T) {
	t.Parallel()
	for _, c := range []holdSpec{
		{stage: "after", reason: "transient", retryable: true, moved: true, age: 5 * time.Second},
		{stage: "cleanup", reason: "lock-lost", retryable: true, moved: true, age: 5 * time.Second},
	} {
		t.Run(c.reason, func(t *testing.T) {
			f := newFx(t)
			f.stoppedHoldingMain(c)
			f.live.SetAlive(41721, "1794132898")
			wt := f.branchWorktree("feature/y")
			var resumed *outcome.Outcome
			d, sleeps, out := f.countingDeps(owner(50002, "w1"), func(n int) {
				if n == 2 { // the owner runs --continue a moment later
					resumed = f.resume(outcome.FlagContinue)
				}
			})
			o := f.waiterRun(d, f.landOf("feature/y", wt))
			want(t, o, outcome.Landed, 0)
			if resumed == nil || resumed.Outcome != outcome.Landed {
				t.Fatalf("the owner's --continue = %+v", resumed)
			}
			if *sleeps < 2 || !strings.Contains(out.String(), "waiting for stopped land feature/x") {
				t.Errorf("sleeps = %d, messages = %q", *sleeps, out.String())
			}
			f.wantSettle("wait", c.stage)
			f.wantLanded("feature/x")
			f.wantLanded("feature/y")
			if f.refExists(locks.WorktreeRef("_main")) {
				t.Error("the main worktree lock survived")
			}
		})
	}
}

func TestWaiterGivesUpWithBusyWhenTheYoungStopIsNotResumedWithinWait(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "cleanup", reason: "lock-lost", retryable: true, moved: true, age: 5 * time.Second})
	wt := f.branchWorktree("feature/y")
	d, sleeps, _ := f.countingDeps(owner(50002, "w1"), nil)
	o := f.waiterRun(d, f.landOf("feature/y", wt))
	// The wait of 3 s runs out: the second phase stops target-busy and names
	// the holder, its stage and the commands.
	f.wantTargetBusy(o)
	if *sleeps < 2 || *sleeps > 4 {
		t.Errorf("sleeps = %d, want about the 3 s wait at 1 s a poll", *sleeps)
	}
	if !strings.Contains(o.Message, "feature/x") || !strings.Contains(o.Message, "cleanup") {
		t.Errorf("message = %s", o.Message)
	}
}

func TestWaiterRefusesAtOnceWhenTheStopNeedsItsOwnersDecision(t *testing.T) {
	t.Parallel()
	cases := map[string]holdSpec{
		"hook failure": {stage: "after", reason: "hook-failed", moved: true, age: 5 * time.Second},
		"conflict":     {stage: "rebasing", reason: "conflict", age: 5 * time.Second},
		"old transient": {
			stage: "cleanup", reason: "transient", retryable: true, moved: true, age: pipeline.YoungStop + time.Second,
		},
		"another host": {
			stage: "cleanup", reason: "transient", retryable: true, moved: true, age: 5 * time.Second, host: "elsewhere.local",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.stoppedHoldingMain(c)
			wt := f.branchWorktree("feature/y")
			d, sleeps, _ := f.countingDeps(owner(50002, "w1"), nil)
			o := f.waiterRun(d, f.landOf("feature/y", wt))
			f.wantTargetBusy(o)
			if *sleeps != 0 {
				t.Errorf("waited %d times for a stop that needs a decision", *sleeps)
			}
			for _, s := range []string{"feature/x", "pg-git-lander land feature/x --continue", "pg-git-lander land feature/x --abort"} {
				if !strings.Contains(o.Message, s) {
					t.Errorf("message lacks %q: %s", s, o.Message)
				}
			}
			if r := f.record(locks.BranchRef("feature/x")); r["state"] != "stopped" {
				t.Errorf("the stopped record was touched: %v", r["state"])
			}
		})
	}
}

// The first lock phase refuses with the owner, branch and command too, and
// with the holder's stage and time in the state.
func TestFirstPhaseRefusalNamesOwnerBranchStageAndCommand(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "after", reason: "hook-failed", moved: true, age: 40 * time.Second})
	// feature/y lands from the main worktree itself, which makes the stopped
	// destination record a first-phase lock.
	f.git(f.repo.Dir, "branch", "feature/y", f.main)
	d := f.deps(owner(50002, "w1"))
	res, err := pipeline.Start(t.Context(), d, pipeline.Request{
		Operation: worktrees.Rebase, Branch: "feature/y", Onto: f.main, Dir: f.repo.Dir, RepoFlag: f.repo.Dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	o := f.wantRefused(res, outcome.RefStoppedPending)
	h := o.Holder
	if h == nil || h.Operation != "land" || h.Branch != "feature/x" || h.Stage != "after" || h.State != "stopped" || h.AgeS < 40 || h.AgeS > 45 {
		t.Fatalf("holder = %+v", h)
	}
	if !strings.Contains(strings.Join(o.Next, "\n"), "pg-git-lander land feature/x --abort --repo "+f.repo.Dir) {
		t.Errorf("next = %v", o.Next)
	}
	if !strings.Contains(o.Human(), "held by land feature/x (after, stopped 40s ago") {
		t.Errorf("human = %s", o.Human())
	}
}

func TestWaiterAbortsAStopWhoseOwnerDiedBeforeTheMove(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "before_move", reason: "holder-died", retryable: true, age: time.Hour})
	tipX := f.tip("feature/x")
	mainBefore := f.tip(f.main)
	wt := f.branchWorktree("feature/y")
	d, _, out := f.countingDeps(owner(50002, "w1"), nil)
	o := f.waiterRun(d, f.landOf("feature/y", wt))
	want(t, o, outcome.Landed, 0)
	f.wantLanded("feature/y")
	if strings.Contains(f.git(f.repo.Dir, "log", "--format=%s", f.main), "add x.txt") {
		t.Error("feature/x was landed, not aborted")
	}
	if f.tip("feature/x") != tipX || f.tip(f.main) == mainBefore {
		t.Error("feature/x must be untouched and main must have moved to feature/y")
	}
	if f.refExists(locks.BranchRef("feature/x")) || f.refExists(locks.WorktreeRef("_main")) {
		t.Errorf("records of the aborted operation remain:\n%s", f.lockRefs())
	}
	if f.workBranch() != "" {
		t.Errorf("work branch left: %s", f.workBranch())
	}
	if !strings.Contains(out.String(), "aborting it") {
		t.Errorf("messages = %q", out.String())
	}
	f.wantSettle("abort", "before_move")
}

func TestWaiterFinishesAStopWhoseOwnerDiedAtOrAfterTheMove(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"moving", "after", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			f := newFx(t)
			f.stoppedHoldingMain(holdSpec{stage: stage, reason: "holder-died", retryable: true, moved: true, age: time.Hour})
			wt := f.branchWorktree("feature/y")
			d, _, out := f.countingDeps(owner(50002, "w1"), nil)
			o := f.waiterRun(d, f.landOf("feature/y", wt))
			want(t, o, outcome.Landed, 0)
			f.wantLanded("feature/x")
			f.wantLanded("feature/y")
			if f.refExists("refs/heads/feature/x") {
				t.Error("the finished land did not retire feature/x")
			}
			if !strings.Contains(out.String(), "finishing it") {
				t.Errorf("messages = %q", out.String())
			}
			f.wantSettle("finish", stage)
		})
	}
}

// A dead owner's stop at stage moving whose move did NOT happen is aborted:
// finishing it would move someone else's branch onto the target.
func TestWaiterAbortsADeadOwnersStopAtMovingWhenTheMoveDidNotHappen(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "moving", reason: "holder-died", retryable: true, age: time.Hour})
	wt := f.branchWorktree("feature/y")
	d, _, out := f.countingDeps(owner(50002, "w1"), nil)
	o := f.waiterRun(d, f.landOf("feature/y", wt))
	want(t, o, outcome.Landed, 0)
	f.wantLanded("feature/y")
	if strings.Contains(f.git(f.repo.Dir, "log", "--format=%s", f.main), "add x.txt") {
		t.Error("the waiter moved the dead owner's branch onto main")
	}
	if !strings.Contains(out.String(), "aborting it") {
		t.Errorf("messages = %q", out.String())
	}
	f.wantSettle("abort", "moving")
}

// When the finish of a dead owner's stop stops again, the waiter says so
// (branch, op id, reason) and leaves a record no later waiter waits on.
func TestWaiterReportsAFailedFinishAndLeavesNothingYoungToWaitOn(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "cleanup", reason: "holder-died", retryable: true, moved: true, age: time.Hour})
	opID := f.record(locks.BranchRef("feature/x"))["op_id"].(string)
	// The clean-up stops again: deleting the work branch fails.
	f.g.Runner = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		if strings.Contains(strings.Join(c.Args, " "), "branch -D") {
			return cmdrun.Result{Code: 1, Stderr: []byte("error: injected\n")}
		}
		return cmdrun.Exec(ctx, c)
	}
	wt := f.branchWorktree("feature/y")
	d, _, _ := f.countingDeps(owner(50002, "w1"), nil)
	o := f.waiterRun(d, f.landOf("feature/y", wt))
	f.wantTargetBusy(o)
	for _, w := range []string{"tried to finish", "feature/x", opID, "stopped again", "git-error"} {
		if !strings.Contains(o.Message, w) {
			t.Errorf("message lacks %q: %s", w, o.Message)
		}
	}
	f.wantSettle("settle-failed", "cleanup")
	rec := f.record(locks.BranchRef("feature/x"))
	if rec["state"] != "stopped" || rec["stop"].(map[string]any)["retryable"] != false {
		t.Fatalf("record after the failed finish = %v", rec)
	}

	// A second waiter, even one that finds the stop young and transient,
	// refuses at once instead of waiting on a dead process.
	f.forge(func(r map[string]any) {
		r["stop"].(map[string]any)["reason"] = "transient"
		r["stop"].(map[string]any)["at"] = f.clock.Now().UTC().Format("2006-01-02T15:04:05Z")
	})
	d2, sleeps, _ := f.countingDeps(owner(50003, "w2"), nil)
	wt2 := f.branchWorktree("feature/z")
	o2 := f.waiterRun(d2, f.landOf("feature/z", wt2))
	f.wantTargetBusy(o2)
	if *sleeps != 0 {
		t.Errorf("the second waiter slept %d times on a record whose finish already failed", *sleeps)
	}
}

func TestWaiterLeavesADeadOwnersStopOnAnotherHostToItsOwner(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "cleanup", reason: "holder-died", retryable: true, moved: true, age: time.Hour, host: "elsewhere.local"})
	wt := f.branchWorktree("feature/y")
	d, sleeps, _ := f.countingDeps(owner(50002, "w1"), nil)
	o := f.waiterRun(d, f.landOf("feature/y", wt))
	f.wantTargetBusy(o)
	if *sleeps != 0 || f.record(locks.BranchRef("feature/x"))["state"] != "stopped" {
		t.Errorf("an owner of unknown liveness must be refused at once and left alone (sleeps %d)", *sleeps)
	}
}

// gateRunner blocks the second `git worktree` call made after arm (the first
// is the repository lookup of the resume's configuration; the second is the
// resume's worktree listing, after it took the BRANCH record back), reporting
// it on reached; it releases when open is closed.
type gateRunner struct {
	mu      sync.Mutex
	armed   bool
	seen    int
	reached chan struct{}
	open    chan struct{}
}

func (g *gateRunner) arm() { g.mu.Lock(); g.armed = true; g.mu.Unlock() }

func (g *gateRunner) run(ctx context.Context, c cmdrun.Call) cmdrun.Result {
	if len(c.Args) > len(gitexec.PinnedOptions) && c.Args[len(gitexec.PinnedOptions)] == "worktree" {
		g.mu.Lock()
		hit := false
		if g.armed {
			g.seen++
			hit = g.seen == 2
			g.armed = !hit
		}
		g.mu.Unlock()
		if hit {
			close(g.reached)
			<-g.open
		}
	}
	return cmdrun.Exec(ctx, c)
}

// Two waiters meet one stopped operation whose owner died after the move. Both
// judge the record before either takes it back; waiter A takes it back and is
// held in the middle of the finish while waiter B tries and loses the
// compare-and-swap (lost-to-concurrent). B goes back to waiting, A finishes
// the operation exactly once and lands, and B then lands too.
func TestTwoWaitersOnADeadOwnersStopHaveOneFinisherAndOneReturnsToWaiting(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.stoppedHoldingMain(holdSpec{stage: "cleanup", reason: "holder-died", retryable: true, moved: true, age: time.Hour})
	wtA := f.branchWorktree("feature/y")
	wtB := f.branchWorktree("feature/z")
	opX := f.record(locks.BranchRef("feature/x"))["op_id"]

	gate := &gateRunner{reached: make(chan struct{}), open: make(chan struct{})}
	ga := *f.g
	ga.Runner = gate.run
	dA, _, outA := f.countingDeps(owner(50002, "wa"), nil)
	dA.Git = &ga
	dA.BeforeSettle = func(string) { gate.arm() }

	bJudged, bGo := make(chan struct{}), make(chan struct{})
	aOut := make(chan *outcome.Outcome, 1)
	dB, _, outB := f.countingDeps(owner(50003, "wb"), nil)
	var once sync.Once
	dB.BeforeSettle = func(string) { once.Do(func() { close(bJudged); <-bGo }) }
	// B's first sleep is its return to waiting: let A go on and finish.
	var opened sync.Once
	var oA *outcome.Outcome
	dB.Sleep = func(_ context.Context, d time.Duration) error {
		f.clock.Advance(d)
		opened.Do(func() {
			close(gate.open)
			oA = <-aOut
		})
		return nil
	}

	// A and B are started (phase one) before the race.
	startA, errA := pipeline.Start(t.Context(), dA, f.landOf("feature/y", wtA))
	startB, errB := pipeline.Start(t.Context(), dB, f.landOf("feature/z", wtB))
	if errA != nil || errB != nil || startA.Prepared == nil || startB.Prepared == nil {
		t.Fatalf("start: %v %v", errA, errB)
	}
	t.Cleanup(func() {
		for _, p := range []*pipeline.Prepared{startA.Prepared, startB.Prepared} {
			_ = p.Release(context.Background())
			_ = p.Close()
		}
	})

	bOut := make(chan *outcome.Outcome, 1)
	go func() {
		o, err := startB.Prepared.Run(t.Context())
		if err != nil {
			t.Error(err)
		}
		bOut <- o
	}()
	<-bJudged // B has judged the record (finish) and waits before taking it back
	go func() {
		o, err := startA.Prepared.Run(t.Context())
		if err != nil {
			t.Error(err)
		}
		aOut <- o
	}()
	<-gate.reached // A took the record back and is inside the finish
	if r := f.record(locks.BranchRef("feature/x")); r["state"] != "running" {
		t.Fatalf("A has not taken the record back: state %v", r["state"])
	}
	close(bGo) // B now tries to take back the same record

	oB := <-bOut
	if oA == nil {
		t.Fatalf("B never went back to waiting; B = %+v, messages %q", oB, outB.String())
	}
	want(t, oA, outcome.Landed, 0)
	want(t, oB, outcome.Landed, 0)
	f.wantLanded("feature/x")
	f.wantLanded("feature/y")
	f.wantLanded("feature/z")
	if !strings.Contains(outB.String(), "taken back by another invocation") {
		t.Errorf("B did not return to waiting: %q", outB.String())
	}
	if strings.Contains(outA.String(), "taken back by another") {
		t.Errorf("A lost the race: %q", outA.String())
	}
	finished := 0
	for _, e := range f.events() {
		if e["op_id"] == opX && e["event"] == "op_end" && e["outcome"] == "landed" {
			finished++
		}
	}
	if finished != 1 {
		t.Errorf("the stopped operation ended %d times, want exactly one finisher", finished)
	}
}
