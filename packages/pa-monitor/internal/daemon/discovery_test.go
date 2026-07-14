package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// TestReapOnce_PrunesDeadBridge verifies that reapOnce delegates to the
// registry's Prune, removing a bridge member whose isAlive check fails
// while leaving a live sibling member (registered for the same cmux server)
// intact.
func TestReapOnce_PrunesDeadBridge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reg := bridge.NewRegistry(30 * time.Second)
	reg.SetNowForTest(func() time.Time { return now })

	noop := func(*pb.DaemonMsg) error { return nil }
	reg.AttachStream(4000, 100, noop) // will report dead
	reg.AttachStream(4000, 200, noop) // will report alive

	alive := map[int]bool{200: true}
	reapOnce(reg, func(pid int) bool { return alive[pid] })

	live, ok := reg.LiveBridge(4000)
	if !ok {
		t.Fatalf("LiveBridge(4000) after reapOnce: got !ok, want ok (200 survives)")
	}
	if live.BridgePID != 200 {
		t.Errorf("LiveBridge(4000) after reapOnce: BridgePID = %d, want 200", live.BridgePID)
	}
}

// TestReapOnce_PrunesAllDeadLeavesServerUnknown verifies that when every
// member for a server is reported dead, the server is fully removed and
// StatusForServer reports Unknown (matching Registry.Prune's contract of
// dropping servers left with no members).
func TestReapOnce_PrunesAllDeadLeavesServerUnknown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reg := bridge.NewRegistry(30 * time.Second)
	reg.SetNowForTest(func() time.Time { return now })

	noop := func(*pb.DaemonMsg) error { return nil }
	reg.AttachStream(5000, 300, noop)

	reapOnce(reg, func(pid int) bool { return false })

	if got := reg.StatusForServer(5000); got != bridge.Unknown {
		t.Errorf("StatusForServer(5000) after reapOnce: got %v, want Unknown", got)
	}
}

// TestRunReaper_TicksAndPrunesUntilCancelled verifies that RunReaper calls
// reapOnce on each tick (pruning dead bridges as they're discovered) and
// returns promptly once ctx is cancelled, without relying on real sleeps
// longer than the tick interval.
func TestRunReaper_TicksAndPrunesUntilCancelled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reg := bridge.NewRegistry(30 * time.Second)
	reg.SetNowForTest(func() time.Time { return now })

	noop := func(*pb.DaemonMsg) error { return nil }
	reg.AttachStream(6000, 400, noop)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	const interval = 5 * time.Millisecond
	go func() {
		RunReaper(ctx, reg, interval, func(pid int) bool { return false })
		close(done)
	}()

	// Poll until the tick has pruned the dead bridge, or fail after a
	// generous bound relative to the interval.
	deadline := time.Now().Add(2 * time.Second)
	for reg.StatusForServer(6000) != bridge.Unknown {

		if time.Now().After(deadline) {
			t.Fatal("RunReaper: dead bridge was not pruned within deadline")
		}
		time.Sleep(interval)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunReaper: did not return after ctx cancellation")
	}
}
