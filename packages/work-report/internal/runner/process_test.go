//go:build unix

package runner

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTimeoutTerminatesTheWholeProcessGroup(t *testing.T) {
	fx := newFixture(t)
	w := newFIFOWatcher(t, fx)
	ready := filepath.Join(t.TempDir(), "ready")
	g := fx.gen(script{Spawn: "same_group", ReadyFile: ready, Hang: true})
	// The timeout starts at process start; it is long enough for the fake to
	// spawn its grandchild first under load, and short enough to keep the
	// test fast. The grandchild's own report through the FIFO is asserted, so
	// an overloaded host fails loudly instead of passing vacuously.
	g.Timeout = 1500 * time.Millisecond
	out := fx.run(g)
	requireKind(t, out, KindTimeout)
	if !out.Record.TimedOut {
		t.Fatal("timed_out is false")
	}
	if out.Record.Signal == nil || *out.Record.Signal != "SIGTERM" {
		t.Fatalf("signal = %v, want SIGTERM (the group got SIGTERM and exited within the grace)", out.Record.Signal)
	}
	if started, _ := w.poll(); !started {
		// The byte may still be in the FIFO only if the holder ran.
		t.Fatal("the grandchild never started; raise the timeout")
	}
	w.waitGone()
	if m := readManifest(t, out.RunDir); m["status"] != "failed" || m["outcome_kind"] != "timeout" || m["timed_out"] != true {
		t.Fatalf("manifest.json = %v", m)
	}
}

func TestGeneratorIgnoringSIGTERMIsKilledAfterTheGrace(t *testing.T) {
	fx := newFixture(t)
	w := newFIFOWatcher(t, fx)
	ready := filepath.Join(t.TempDir(), "ready")
	fx.runner.Grace = 150 * time.Millisecond
	g := fx.gen(script{IgnoreTerm: true, Spawn: "same_group", ReadyFile: ready, Hang: true})

	cancel, result := fx.runAsync(g)
	waitForFile(t, ready)
	w.waitStarted()
	cancel()
	out := result()

	requireKind(t, out, KindCancelled)
	if out.Record.TimedOut {
		t.Fatal("timed_out is true for a cancellation")
	}
	if out.Record.Signal == nil || *out.Record.Signal != "SIGKILL" {
		t.Fatalf("signal = %v, want SIGKILL after the grace", out.Record.Signal)
	}
	if got := time.Duration(out.Record.GeneratorMS) * time.Millisecond; got < 100*time.Millisecond {
		t.Fatalf("generator_ms = %d: SIGKILL came before the grace period elapsed", out.Record.GeneratorMS)
	}
	w.waitGone()
}

func TestCancellationBeforeGraceKeepsTheSIGTERMExit(t *testing.T) {
	fx := newFixture(t)
	ready := filepath.Join(t.TempDir(), "ready")
	fx.runner.Grace = 30 * time.Second // a SIGKILL here would be a test hang
	g := fx.gen(script{ReadyFile: ready, Hang: true})
	cancel, result := fx.runAsync(g)
	waitForFile(t, ready)
	cancel()
	out := result()
	requireKind(t, out, KindCancelled)
	if out.Record.Signal == nil || *out.Record.Signal != "SIGTERM" {
		t.Fatalf("signal = %v, want SIGTERM", out.Record.Signal)
	}
}

func TestGrandchildHoldingThePipeCannotHangTheRun(t *testing.T) {
	fx := newFixture(t)
	w := newFIFOWatcher(t, fx)
	s := okScript()
	s.Spawn = "hold_pipe" // the grandchild inherits stdout and stderr and outlives the generator
	fx.runner.WaitDelay = 200 * time.Millisecond
	begin := time.Now()
	out := fx.run(fx.gen(s))
	requireOK(t, out)
	if elapsed := time.Since(begin); elapsed > 20*time.Second {
		t.Fatalf("run took %s: the held pipe delayed it", elapsed)
	}
	w.waitStarted()
	w.waitGone()
}

func TestLeftoverGroupMembersAreKilledOnNormalExit(t *testing.T) {
	fx := newFixture(t)
	w := newFIFOWatcher(t, fx)
	s := okScript()
	s.Spawn = "same_group" // does not hold stdout or stderr
	out := fx.run(fx.gen(s))
	requireOK(t, out)
	if out.Record.Signal != nil {
		t.Fatalf("signal = %v", *out.Record.Signal)
	}
	w.waitStarted()
	w.waitGone()
}

func TestContextAlreadyCancelledStillRecordsCancelled(t *testing.T) {
	fx := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := fx.gen(script{Hang: true})
	out := fx.runner.Run(ctx, fx.request(g))
	requireKind(t, out, KindCancelled)
}
