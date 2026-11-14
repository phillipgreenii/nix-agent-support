package bridge

import (
	"testing"
	"time"
)

func TestRegistry_UnknownByDefault(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	if got := r.StatusForServer(1234); got != Unknown {
		t.Errorf("unregistered server: got %v, want Unknown", got)
	}
}

func TestRegistry_StaleAfterCutoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.AttachStream(4000, 100)

	r.now = func() time.Time { return now.Add(31 * time.Second) }
	if got := r.StatusForServer(4000); got != Stale {
		t.Errorf("after staleAfter cutoff: got %v, want Stale", got)
	}

	r.now = func() time.Time { return now.Add(29 * time.Second) }
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("just under staleAfter cutoff: got %v, want Alive", got)
	}
}

func TestRegistry_ReAttachRefreshesLastSeen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.AttachStream(4000, 100)

	r.now = func() time.Time { return now.Add(45 * time.Second) }
	if got := r.StatusForServer(4000); got != Stale {
		t.Fatalf("precondition: should be stale at 45s; got %v", got)
	}

	r.AttachStream(4000, 100)
	if got := r.StatusForServer(4000); got != Alive {
		t.Errorf("after re-attach: got %v, want Alive", got)
	}
}

func TestRegistry_DistinctServersTrackedIndependently(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.AttachStream(4000, 100)

	r.now = func() time.Time { return now.Add(40 * time.Second) }
	r.AttachStream(5000, 200)

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

	r.AttachStream(4000, 100)
	r.AttachStream(4000, 200)

	if got := r.BridgePIDs(4000); len(got) != 2 {
		t.Fatalf("BridgePIDs(4000) = %v, want both 100 and 200", got)
	}

	// Both bridges retained: deregistering one leaves the other tracked as a
	// set member.
	r.Deregister(4000, 100)
	if got := r.BridgePIDs(4000); len(got) != 1 || got[0] != 200 {
		t.Errorf("BridgePIDs(4000) after deregistering 100 = %v, want [200]", got)
	}

	r.Deregister(4000, 200)
	if got := r.BridgePIDs(4000); got != nil {
		t.Errorf("BridgePIDs(4000) after deregistering both = %v, want nil", got)
	}
}

func TestRegistry_StatusForServerStaleOverSetOfBridges(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream(4000, 100)

	r.now = func() time.Time { return now.Add(10 * time.Second) }
	r.AttachStream(4000, 200)

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

	r.AttachStream(4000, 100)

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

	r.AttachStream(4000, 100)
	r.Deregister(4000, 100)

	if got := r.BridgePIDs(4000); got != nil {
		t.Errorf("BridgePIDs(4000) after Deregister = %v, want nil", got)
	}
	if got := r.StatusForServer(4000); got != Unknown {
		t.Errorf("StatusForServer(4000) after Deregister of only member: got %v, want Unknown", got)
	}
}

func TestRegistry_PruneDropsDeadBridgePIDs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream(4000, 100)
	r.AttachStream(4000, 200)

	alive := map[int]bool{200: true}
	r.Prune(func(pid int) bool { return alive[pid] })

	if got := r.BridgePIDs(4000); len(got) != 1 || got[0] != 200 {
		t.Errorf("BridgePIDs(4000) after Prune = %v, want [200]", got)
	}
}

func TestRegistry_BridgePIDsSortedAndEmptyWhenNone(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	if got := r.BridgePIDs(4000); got != nil {
		t.Errorf("BridgePIDs(4000) with no members = %v, want nil", got)
	}
	r.AttachStream(4000, 300)
	r.AttachStream(4000, 100)
	r.AttachStream(5000, 900)
	got := r.BridgePIDs(4000)
	if len(got) != 2 || got[0] != 100 || got[1] != 300 {
		t.Errorf("BridgePIDs(4000) = %v, want [100 300]", got)
	}
}
