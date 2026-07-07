package bridge

import (
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

func TestRegistry_UnknownByDefault(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	if got := r.StatusForServer(1234); got != Unknown {
		t.Errorf("unregistered server: got %v, want Unknown", got)
	}
}

func TestRegistry_RegisterMarksAlive(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(4000)
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("fresh registration: got %v, want Alive", got)
	}
}

func TestRegistry_StaleAfterCutoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.Register(4000)

	r.now = func() time.Time { return now.Add(31 * time.Second) }
	if got := r.StatusForServer(4000); got != Stale {
		t.Errorf("after staleAfter cutoff: got %v, want Stale", got)
	}

	r.now = func() time.Time { return now.Add(29 * time.Second) }
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("just under staleAfter cutoff: got %v, want Alive", got)
	}
}

func TestRegistry_ReRegisterRefreshesLastSeen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.Register(4000)

	r.now = func() time.Time { return now.Add(45 * time.Second) }
	if got := r.StatusForServer(4000); got != Stale {
		t.Fatalf("precondition: should be stale at 45s; got %v", got)
	}

	r.Register(4000)
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("after re-register: got %v, want Alive", got)
	}
}

func TestRegistry_DistinctServersTrackedIndependently(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.Register(4000)

	r.now = func() time.Time { return now.Add(40 * time.Second) }
	r.Register(5000)

	if got := r.StatusForServer(4000); got != Stale {
		t.Errorf("ws-A at t=40s: got %v, want Stale", got)
	}
	if got := r.StatusForServer(5000); got != Alive {
		t.Errorf("ws-B at t=40s (just registered): got %v, want Alive", got)
	}
}

func TestRegistry_AttachStreamRetainsMultipleBridgesPerServer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	sendCalls := map[int]int{}
	send := func(pid int) func(*pb.DaemonMsg) error {
		return func(*pb.DaemonMsg) error {
			sendCalls[pid]++
			return nil
		}
	}
	r.AttachStream(4000, 100, send(100))
	r.AttachStream(4000, 200, send(200))

	live, ok := r.LiveBridge(4000)
	if !ok {
		t.Fatalf("LiveBridge(4000): got !ok, want a live member")
	}
	if live.send == nil {
		t.Fatalf("LiveBridge(4000): send hook is nil")
	}
	if live.BridgePID != 100 && live.BridgePID != 200 {
		t.Errorf("LiveBridge(4000).BridgePID = %d, want 100 or 200", live.BridgePID)
	}

	// Both bridges retained: deregistering one leaves the other still
	// discoverable through the registry itself (not just the local
	// closures), proving both were actually tracked as set members.
	r.Deregister(4000, 100)
	live, ok = r.LiveBridge(4000)
	if !ok {
		t.Fatalf("LiveBridge(4000) after deregistering 100: got !ok, want ok (200 survives)")
	}
	if live.BridgePID != 200 {
		t.Errorf("LiveBridge(4000) after deregistering 100: BridgePID = %d, want 200", live.BridgePID)
	}

	r.Deregister(4000, 200)
	if _, ok := r.LiveBridge(4000); ok {
		t.Errorf("LiveBridge(4000) after deregistering both: got ok, want !ok")
	}
}

func TestRegistry_StatusForServerStaleOverSetOfBridges(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	noop := func(*pb.DaemonMsg) error { return nil }
	r.AttachStream(4000, 100, noop)

	r.now = func() time.Time { return now.Add(10 * time.Second) }
	r.AttachStream(4000, 200, noop)

	// Freshest member (bridgePID 200) is 10s old; still Alive.
	r.now = func() time.Time { return now.Add(20 * time.Second) }
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("freshest member 10s old: got %v, want Alive", got)
	}

	// Both members now aged past staleAfter from their respective lastSeen.
	r.now = func() time.Time { return now.Add(41 * time.Second) }
	if got := r.StatusForServer(4000); got != Stale {
		t.Errorf("both members past staleAfter: got %v, want Stale", got)
	}
}

func TestRegistry_HeartbeatRefreshesMember(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	noop := func(*pb.DaemonMsg) error { return nil }
	r.AttachStream(4000, 100, noop)

	r.Heartbeat(4000, 100, now.Add(45*time.Second))
	r.now = func() time.Time { return now.Add(50 * time.Second) }
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("after heartbeat refresh: got %v, want Alive", got)
	}
}

func TestRegistry_DeregisterRemovesMember(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	noop := func(*pb.DaemonMsg) error { return nil }
	r.AttachStream(4000, 100, noop)
	r.Deregister(4000, 100)

	if _, ok := r.LiveBridge(4000); ok {
		t.Errorf("LiveBridge(4000) after Deregister: got ok, want !ok")
	}
	if got := r.StatusForServer(4000); got != Unknown {
		t.Errorf("StatusForServer(4000) after Deregister of only member: got %v, want Unknown", got)
	}
}

func TestRegistry_PruneDropsDeadBridgePIDs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	noop := func(*pb.DaemonMsg) error { return nil }
	r.AttachStream(4000, 100, noop)
	r.AttachStream(4000, 200, noop)

	alive := map[int]bool{200: true}
	r.Prune(func(pid int) bool { return alive[pid] })

	live, ok := r.LiveBridge(4000)
	if !ok {
		t.Fatalf("LiveBridge(4000) after Prune: got !ok, want ok (200 survives)")
	}
	if live.BridgePID != 200 {
		t.Errorf("LiveBridge(4000) after Prune: BridgePID = %d, want 200", live.BridgePID)
	}
}

func TestRegistry_LiveBridgeIgnoresDisplayOnlyRegisterMember(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.Register(4000)
	if _, ok := r.LiveBridge(4000); ok {
		t.Errorf("LiveBridge(4000) with only a display-only Register member: got ok, want !ok")
	}
}
