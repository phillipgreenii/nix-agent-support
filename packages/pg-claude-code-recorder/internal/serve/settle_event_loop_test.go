package serve

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func wakeCount(d *Daemon, reason string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MSettleWakes, map[string]string{"reason": reason})
	return v
}

// The default mode is the scan: no event loop state is built and the loop runs
// Tick on the minute ticker.
func TestSettleModeDefaultsToScan(t *testing.T) {
	_, d, _ := seamDaemon(t, nil)
	if d.sett.EventMode() {
		t.Fatal("the default settle mode is not scan")
	}
}

// The event loop makes its first pass (the reconciliation) only after the first
// parse, then wakes on the poll timer, the reconcile timer and the dirty signal,
// each counted under its own reason, never sooner than min_wake after a wake.
func TestEventSettleLoopWakesOnItsTriggers(t *testing.T) {
	_, d, _ := fakeClockDaemon(t, func(e *env) {
		e.cfg.SettleMode = "event"
		e.cfg.SettleMinWake = time.Second // the fake clock never advances: every wake waits the coalesce timer
	})
	f := newFakeTicks(d)
	goLoop(d, d.settleLoop)
	// Before the first parse nothing runs.
	select {
	case l := <-f.done:
		t.Fatalf("pass %s before the first parse", l)
	case <-time.After(50 * time.Millisecond):
	}
	d.firstParseOnce.Do(func() { close(d.firstParse) })
	f.await(t, obs.LoopSettleTick) // the start-up reconciliation
	if got := wakeCount(d, "reconcile"); got != 1 {
		t.Fatalf("reconcile wakes after start = %v, want 1", got)
	}

	send := func(seam string) {
		t.Helper()
		select {
		case f.ch(seam) <- time.Time{}:
		case <-time.After(10 * time.Second):
			t.Fatalf("the loop did not take %s", seam)
		}
	}
	// A trigger starts a wake only after the min-wake wait.
	send(settlePollSeam)
	send(settleCoalesceSeam)
	f.await(t, obs.LoopSettleTick)
	if got := wakeCount(d, "poll"); got != 1 {
		t.Fatalf("poll wakes = %v, want 1", got)
	}
	d.sett.LiveChanged() // what the tracker's change callback does
	send(settleCoalesceSeam)
	f.await(t, obs.LoopSettleTick)
	if got := wakeCount(d, "dirty"); got != 1 {
		t.Fatalf("dirty wakes = %v, want 1", got)
	}
	send(settleReconcileSeam)
	f.await(t, obs.LoopSettleTick)
	if got := wakeCount(d, "reconcile"); got != 2 {
		t.Fatalf("reconcile wakes = %v, want 2", got)
	}
	// Each pass was one settle_tick run, so the wake duration histogram is the
	// tick duration histogram.
	if h := d.obs.Reg.Hist(obs.MSettleTickDuration); h == nil || h.Samples != 4 {
		t.Fatalf("settle_tick_duration samples %+v, want 4 (start, poll, dirty, reconcile)", h)
	}
}
