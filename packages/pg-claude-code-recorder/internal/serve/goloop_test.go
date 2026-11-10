package serve

import (
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func inFlightNames(d *Daemon) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, f := range d.inFlight() {
		out[f.Name] = f.Age
	}
	return out
}

// Every goLoop goroutine returns once the root context is cancelled, and while
// they run each is visible by name with an age, including one blocked outside
// runLoop (which loopTracker never sees).
func TestGoLoopStopsLoopsAndNamesInFlight(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	e := newEnv(t, false)
	d := e.open(t)

	started := make(chan string, 3)
	d.goLoop("plain", func() { started <- "plain"; <-d.stop })
	d.goLoop("in_run_loop", func() {
		_ = d.runLoop("test_loop", func() error { started <- "in_run_loop"; <-d.stop; return nil })
	})
	release := make(chan struct{})
	d.goLoop("outside_run_loop", func() {
		started <- "outside_run_loop"
		select {
		case <-d.ctx.Done():
		case <-release:
		}
	})
	for range 3 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("loops did not start")
		}
	}
	time.Sleep(5 * time.Millisecond)
	got := inFlightNames(d)
	for _, n := range []string{"plain", "in_run_loop", "outside_run_loop"} {
		age, ok := got[n]
		if !ok || age <= 0 {
			t.Fatalf("inFlight %v lacks %q with a positive age", got, n)
		}
	}

	d.cancel(errDaemonClosing)
	deadline := time.Now().Add(5 * time.Second)
	for len(d.inFlight()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("loops still running after cancel: %v", d.inFlight())
		}
		time.Sleep(time.Millisecond)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := goleak.Find(opt); err != nil {
		t.Fatalf("goroutines leaked past Close: %v", err)
	}
}

// goLoop refuses once Close has begun, and Close does not return before a loop
// that is already running has.
func TestGoLoopRefusedWhileClosing(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	d.beginClosing()
	ran := make(chan struct{}, 1)
	if d.goLoop("late", func() { ran <- struct{}{} }) {
		t.Fatal("goLoop accepted a loop while closing")
	}
	select {
	case <-ran:
		t.Fatal("refused loop ran")
	case <-time.After(20 * time.Millisecond):
	}
	_ = d.Close()
}

func TestOpSweepRefusedWhileClosing(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	d.beginClosing()
	ans, werr := d.opSweep(nil, []byte(`{"force":true}`))
	if werr == nil || werr.Name != wire.ErrUnavailable {
		t.Fatalf("answer %+v err %v, want an unavailable refusal", ans, werr)
	}
	if d.forceRunning || d.forcePending.Load() {
		t.Fatal("a refused sweep left the forced-sweep state set")
	}
	_ = d.Close()
}

// A daemon built as a literal, with no Open, has no root context; loops that
// ask for one get a never-cancelled context instead of a nil.
func TestRootCtxToleratesLiteralDaemon(t *testing.T) {
	d := &Daemon{}
	if d.rootCtx() == nil || d.rootCtx().Err() != nil {
		t.Fatal("literal daemon must get a live background context")
	}
}
