package pipeline_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Tests of the signal handling (design section 6.4): real git, a real hook
// script in its own process group, signals delivered through the
// Interrupter's channel (no signal reaches the test process).

// graceful makes the fixture's git execution layer end a step's process group
// with SIGTERM, then SIGKILL after grace.
func (f *fx) graceful(grace time.Duration) { f.g.Runner = pipeline.GroupRunner(grace) }

// waitFile waits for a file a hook writes once it runs.
func waitFile(t *testing.T, p string) string {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(p); err == nil && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
	}
	t.Fatalf("%s never appeared", p)
	return ""
}

func gone(pid int) bool {
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
	}
	return false
}

func newSignals() chan os.Signal { return make(chan os.Signal, 4) }

// blockingHook is a before_move hook that starts a grandchild, records the
// grandchild's pid and waits.
func (f *fx) blockingHook(pidFile, prefix string) {
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript(
		prefix + "\nsleep 120 &\necho $! > " + pidFile + "\nwait",
	)}, "")
}

func TestSignalStopsTheRunningHookGroupAndYieldsInterrupted(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.graceful(5 * time.Second)
	mainTip, featTip := f.tip(f.main), f.tip("feature/x")
	pidFile := filepath.Join(t.TempDir(), "pid")
	f.blockingHook(pidFile, "")
	sigs := newSignals()
	ctx, stop := pipeline.Interrupter{Signals: sigs, Exit: func(int) { t.Error("exit called on a first signal") }}.Context(t.Context())
	defer stop()
	p := f.prepared(f.land())
	go func() {
		waitFile(t, pidFile)
		sigs <- syscall.SIGTERM
	}()
	out, err := p.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.wantStopped(out, outcome.StopInterrupted)
	pid, _ := strconv.Atoi(waitFile(t, pidFile))
	if !gone(pid) {
		t.Fatal("the hook's process group survived the signal")
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("an interrupted hook moved a branch")
	}
}

func TestSignalSendsSIGKILLAfterTheGraceToAGroupThatIgnoresSIGTERM(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.graceful(300 * time.Millisecond)
	pidFile := filepath.Join(t.TempDir(), "pid")
	f.blockingHook(pidFile, "trap '' TERM")
	sigs := newSignals()
	ctx, stop := pipeline.Interrupter{Signals: sigs}.Context(t.Context())
	defer stop()
	p := f.prepared(f.land())
	go func() {
		waitFile(t, pidFile)
		sigs <- syscall.SIGHUP
	}()
	start := time.Now()
	out, err := p.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.wantStopped(out, outcome.StopInterrupted)
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("the group ended after %v: SIGTERM was not given its grace", d)
	}
	pid, _ := strconv.Atoi(waitFile(t, pidFile))
	if !gone(pid) {
		t.Fatal("the group that ignores SIGTERM survived the SIGKILL")
	}
}

// onMove answers every git call with the real runner, but delivers sig just
// before the fenced ref transaction of the move to TARGET starts.
func (f *fx) signalAtTheMove(sigs chan os.Signal) {
	var once sync.Once
	real := pipeline.GroupRunner(time.Second)
	f.g.Runner = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		args := strings.Join(c.Args, " ")
		if strings.Contains(args, "merge --ff-only") ||
			(strings.Contains(args, "update-ref --stdin") && strings.Contains(string(c.Stdin), "refs/heads/"+f.main)) {
			once.Do(func() {
				sigs <- syscall.SIGINT
				time.Sleep(200 * time.Millisecond) // the Interrupter has cancelled the context by now
			})
		}
		return real(ctx, c)
	}
}

func TestSignalWhileTheMoveTransactionStartsLetsItFinish(t *testing.T) {
	f := newFx(t)
	f.feature()
	featTip := f.tip("feature/x")
	sigs := newSignals()
	f.signalAtTheMove(sigs)
	ctx, stop := pipeline.Interrupter{Signals: sigs}.Context(t.Context())
	defer stop()
	p := f.prepared(f.land())
	out, err := p.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.tip(f.main) != featTip {
		t.Fatalf("the in-flight move did not finish: %s, want %s", f.tip(f.main), featTip)
	}
	want(t, out, outcome.Stopped, 4)
	if out.Reason != string(outcome.StopInterrupted) || out.MovedAlready == nil || !*out.MovedAlready {
		t.Fatalf("outcome = %s (%s) moved_already %v", out.Reason, out.Message, out.MovedAlready)
	}
	if r := f.record(locks.BranchRef("feature/x")); r["state"] != "stopped" {
		t.Fatalf("BRANCH record = %v", r)
	}
}

func TestSecondSignalExitsAtOnceLeavingTheRecordRunning(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.graceful(600 * time.Millisecond)
	pidFile := filepath.Join(t.TempDir(), "pid")
	f.blockingHook(pidFile, "trap '' TERM")
	sigs := newSignals()
	type seen struct {
		code  int
		state string
	}
	exited := make(chan seen, 1)
	in := pipeline.Interrupter{Signals: sigs, Exit: func(code int) {
		// What a real exit would leave behind: read the record at this instant.
		r := f.g.Run(context.Background(), f.repo.Dir, "cat-file", "-p", locks.BranchRef("feature/x"))
		var rec map[string]any
		st := "unreadable"
		if json.Unmarshal(r.Stdout, &rec) == nil {
			st, _ = rec["state"].(string)
		}
		exited <- seen{code, st}
	}}
	ctx, stop := in.Context(t.Context())
	defer stop()
	p := f.prepared(f.land())
	go func() {
		waitFile(t, pidFile)
		sigs <- syscall.SIGINT
		sigs <- syscall.SIGINT
	}()
	if _, err := p.Run(ctx); err != nil { // the test process survives the "exit", so Run ends once the group is killed
		t.Fatal(err)
	}
	select {
	case s := <-exited:
		if s.code != pipeline.SecondSignalExit || s.state != "running" {
			t.Fatalf("second signal: exit %d with the record %s, want exit %d with it running", s.code, s.state, pipeline.SecondSignalExit)
		}
	default:
		t.Fatal("a second signal did not exit")
	}
}

func TestSignalDuringContinueEndsAsInterruptedStop(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("exit 1")}, "")
	f.wantStopped(f.run(f.land()), outcome.StopHookFailed)
	f.graceful(5 * time.Second)
	pidFile := filepath.Join(t.TempDir(), "pid")
	f.blockingHook(pidFile, "")
	sigs := newSignals()
	ctx, stop := pipeline.Interrupter{Signals: sigs}.Context(t.Context())
	defer stop()
	go func() {
		waitFile(t, pidFile)
		sigs <- syscall.SIGTERM
	}()
	o, err := pipeline.Resume(ctx, f.deps(owner(50001, "r1")), f.resumeReq(outcome.FlagContinue, worktrees.Land, "feature/x"))
	if err != nil {
		t.Fatal(err)
	}
	f.wantStopped(o, outcome.StopInterrupted)
	pid, _ := strconv.Atoi(waitFile(t, pidFile))
	if !gone(pid) {
		t.Fatal("the resumed hook's group survived")
	}
	// The interrupted resume is itself resumable.
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("true")}, "")
	want(t, f.resume(outcome.FlagContinue), outcome.Landed, 0)
}
