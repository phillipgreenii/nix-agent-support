package main

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// launchdExitTimeMargin is the margin the deployment keeps between the
// shutdown deadline and launchd's ExitTimeOut (the nix module derives
// ExitTimeOut as deadline plus this margin), and launchdExitTimeClamp is the
// value launchd silently clamps ExitTimeOut to.
const (
	launchdExitTimeMargin = 7 * time.Second
	launchdExitTimeClamp  = 60 * time.Second
)

// The phases share ONE deadline: whatever the deployment passes, the budgets of
// the phases sum to strictly less than it, so the watchdog never fires on a
// shutdown whose every phase used its whole share.
func TestShutdownBudget_phasesSumUnderDeadline(t *testing.T) {
	for _, deadline := range []time.Duration{time.Second, 8 * time.Second, 30 * time.Second, defaultShutdownDeadline, launchdExitTimeClamp - launchdExitTimeMargin} {
		b := newShutdownBudget(deadline)
		if b.Deadline != deadline {
			t.Fatalf("deadline %v: budget carries %v", deadline, b.Deadline)
		}
		for name, d := range map[string]time.Duration{"drain": b.Drain, "preShutdown": b.PreShutdown, "tail": b.Tail, "flush": b.Flush} {
			if d <= 0 {
				t.Fatalf("deadline %v: %s share = %v, want positive", deadline, name, d)
			}
		}
		if b.Sum() >= deadline {
			t.Fatalf("deadline %v: phases sum to %v, want strictly under", deadline, b.Sum())
		}
	}
}

// The default deadline plus the module's margin stays inside launchd's clamp,
// and the drain keeps the figure the daemon always had (about 20 s).
func TestShutdownBudget_defaultFitsLaunchdAndKeepsDrain(t *testing.T) {
	if defaultShutdownDeadline+launchdExitTimeMargin > launchdExitTimeClamp {
		t.Fatalf("default deadline %v plus margin %v exceeds launchd's %v clamp", defaultShutdownDeadline, launchdExitTimeMargin, launchdExitTimeClamp)
	}
	b := newShutdownBudget(0)
	if b.Deadline != defaultShutdownDeadline {
		t.Fatalf("zero deadline selects %v, want %v", b.Deadline, defaultShutdownDeadline)
	}
	if b.Drain < 19*time.Second || b.Drain > 21*time.Second {
		t.Fatalf("default drain = %v, want about 20s", b.Drain)
	}
}

// A phase takes min(its share, what is left of the deadline): an earlier
// overrun shrinks the later phases instead of pushing past the deadline.
func TestShutdownClock_withinNeverOutlivesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		clk := newShutdownClock(ctx, 15*time.Second)
		if got := clk.Within(10 * time.Second); got != 10*time.Second {
			t.Fatalf("before the signal: %v, want the whole share", got)
		}
		if got := clk.Within(20 * time.Second); got != 15*time.Second {
			t.Fatalf("a share over the deadline is capped at it: %v", got)
		}
		cancel()
		synctest.Wait()
		time.Sleep(8 * time.Second)
		if got := clk.Within(10 * time.Second); got != 7*time.Second {
			t.Fatalf("8s into a 15s deadline: %v, want 7s left", got)
		}
		time.Sleep(10 * time.Second)
		if got := clk.Within(10 * time.Second); got != 0 {
			t.Fatalf("past the deadline: %v, want 0", got)
		}
	})
}

// --shutdown-deadline is accepted by run and run-until-idle and reaches the
// route result; omitted it is zero, which selects the default.
func TestParseRunLikeArgs_shutdownDeadline(t *testing.T) {
	for _, kind := range []routeKind{routeRun, routeRunUntilIdle} {
		if got := parseRunLikeArgs(kind, []string{"--shutdown-deadline", "45s"}); got.kind != kind || got.shutdownDeadline != 45*time.Second {
			t.Fatalf("kind %v: got kind %v deadline %v", kind, got.kind, got.shutdownDeadline)
		}
		if got := parseRunLikeArgs(kind, nil).shutdownDeadline; got != 0 {
			t.Fatalf("kind %v: omitted deadline = %v, want 0", kind, got)
		}
	}
	if got := parseRunLikeArgs(routeRun, []string{"--shutdown-deadline", "soon"}).kind; got != routeUsageErr {
		t.Fatalf("a non-duration deadline: kind %v, want usage error", got)
	}
}
