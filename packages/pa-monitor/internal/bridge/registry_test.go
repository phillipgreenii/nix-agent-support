package bridge

import (
	"testing"
	"time"
)

func TestRegistry_UnknownByDefault(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	if got := r.StatusForWorkspace("ws-1234"); got != Unknown {
		t.Errorf("unregistered server: got %v, want Unknown", got)
	}
}

func TestRegistry_StaleAfterCutoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.AttachStream("ws-4000", 4000, 100)

	r.now = func() time.Time { return now.Add(31 * time.Second) }
	if got := r.StatusForWorkspace("ws-4000"); got != Stale {
		t.Errorf("after staleAfter cutoff: got %v, want Stale", got)
	}

	r.now = func() time.Time { return now.Add(29 * time.Second) }
	if got := r.StatusForWorkspace("ws-4000"); got != Alive {
		t.Errorf("just under staleAfter cutoff: got %v, want Alive", got)
	}
}

func TestRegistry_ReAttachRefreshesLastSeen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.AttachStream("ws-4000", 4000, 100)

	r.now = func() time.Time { return now.Add(45 * time.Second) }
	if got := r.StatusForWorkspace("ws-4000"); got != Stale {
		t.Fatalf("precondition: should be stale at 45s; got %v", got)
	}

	r.AttachStream("ws-4000", 4000, 100)
	if got := r.StatusForWorkspace("ws-4000"); got != Alive {
		t.Errorf("after re-attach: got %v, want Alive", got)
	}
}

func TestRegistry_DistinctServersTrackedIndependently(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }
	r.AttachStream("ws-4000", 4000, 100)

	r.now = func() time.Time { return now.Add(40 * time.Second) }
	r.AttachStream("ws-5000", 5000, 200)

	if got := r.StatusForWorkspace("ws-4000"); got != Stale {
		t.Errorf("ws-A at t=40s: got %v, want Stale", got)
	}
	if got := r.StatusForWorkspace("ws-5000"); got != Alive {
		t.Errorf("ws-B at t=40s (just registered): got %v, want Alive", got)
	}
}

func TestRegistry_AttachStreamRetainsMultipleBridgesPerServer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream("ws-4000", 4000, 100)
	r.AttachStream("ws-4000", 4000, 200)

	if got := r.BridgePIDs("ws-4000"); len(got) != 2 {
		t.Fatalf("BridgePIDs(ws-4000) = %v, want both 100 and 200", got)
	}

	// Both bridges retained: deregistering one leaves the other tracked as a
	// set member.
	r.Deregister("ws-4000", 100)
	if got := r.BridgePIDs("ws-4000"); len(got) != 1 || got[0] != 200 {
		t.Errorf("BridgePIDs(ws-4000) after deregistering 100 = %v, want [200]", got)
	}

	r.Deregister("ws-4000", 200)
	if got := r.BridgePIDs("ws-4000"); got != nil {
		t.Errorf("BridgePIDs(ws-4000) after deregistering both = %v, want nil", got)
	}
}

func TestRegistry_StatusForWorkspaceStaleOverSetOfBridges(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream("ws-4000", 4000, 100)

	r.now = func() time.Time { return now.Add(10 * time.Second) }
	r.AttachStream("ws-4000", 4000, 200)

	// Freshest member (bridgePID 200) is 10s old; still Alive.
	r.now = func() time.Time { return now.Add(20 * time.Second) }
	if got := r.StatusForWorkspace("ws-4000"); got != Alive {
		t.Errorf("freshest member 10s old: got %v, want Alive", got)
	}

	// Both members now aged past staleAfter from their respective lastSeen.
	r.now = func() time.Time { return now.Add(41 * time.Second) }
	if got := r.StatusForWorkspace("ws-4000"); got != Stale {
		t.Errorf("both members past staleAfter: got %v, want Stale", got)
	}
}

func TestRegistry_HeartbeatRefreshesMember(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream("ws-4000", 4000, 100)

	r.Heartbeat("ws-4000", 100, now.Add(45*time.Second))
	r.now = func() time.Time { return now.Add(50 * time.Second) }
	if got := r.StatusForWorkspace("ws-4000"); got != Alive {
		t.Errorf("after heartbeat refresh: got %v, want Alive", got)
	}
}

func TestRegistry_DeregisterRemovesMember(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream("ws-4000", 4000, 100)
	r.Deregister("ws-4000", 100)

	if got := r.BridgePIDs("ws-4000"); got != nil {
		t.Errorf("BridgePIDs(ws-4000) after Deregister = %v, want nil", got)
	}
	if got := r.StatusForWorkspace("ws-4000"); got != Unknown {
		t.Errorf("StatusForWorkspace(ws-4000) after Deregister of only member: got %v, want Unknown", got)
	}
}

func TestRegistry_PruneDropsDeadBridgePIDs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	r.AttachStream("ws-4000", 4000, 100)
	r.AttachStream("ws-4000", 4000, 200)

	alive := map[int]bool{200: true}
	r.Prune(func(pid int) bool { return alive[pid] })

	if got := r.BridgePIDs("ws-4000"); len(got) != 1 || got[0] != 200 {
		t.Errorf("BridgePIDs(ws-4000) after Prune = %v, want [200]", got)
	}
}

func TestRegistry_BridgePIDsSortedAndEmptyWhenNone(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	if got := r.BridgePIDs("ws-4000"); got != nil {
		t.Errorf("BridgePIDs(ws-4000) with no members = %v, want nil", got)
	}
	r.AttachStream("ws-4000", 4000, 300)
	r.AttachStream("ws-4000", 4000, 100)
	r.AttachStream("ws-5000", 5000, 900)
	got := r.BridgePIDs("ws-4000")
	if len(got) != 2 || got[0] != 100 || got[1] != 300 {
		t.Errorf("BridgePIDs(ws-4000) = %v, want [100 300]", got)
	}
}

func TestRegistry_ReconnectByWorkspaceIDAndDisconnectLabels(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return now }

	if got := r.StatusForWorkspace("ws-a"); got != Unknown {
		t.Fatalf("never registered: got %v, want Unknown", got)
	}
	r.AttachStream("ws-a", 4000, 100)
	if got := r.StatusForWorkspace("ws-a"); got != Alive {
		t.Fatalf("registered: got %v, want Alive", got)
	}
	// Disconnect: the stream closes and deregisters; no member remains.
	r.Deregister("ws-a", 100)
	if got := r.StatusForWorkspace("ws-a"); got != Unknown {
		t.Fatalf("after disconnect: got %v, want Unknown", got)
	}
	// Reconnect under a new bridge pid and a new server pid (cmux restarted):
	// the workspace id still keys the registration.
	r.AttachStream("ws-a", 4999, 101)
	if got := r.StatusForWorkspace("ws-a"); got != Alive {
		t.Errorf("after reconnect: got %v, want Alive", got)
	}
}

// A bridge registered under another workspace id must not satisfy this lookup,
// even when both report the same cmux server pid.
func TestRegistry_OtherWorkspaceDoesNotSatisfyLookup(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.AttachStream("ws-b", 4000, 100)
	if got := r.StatusForWorkspace("ws-a"); got != Unknown {
		t.Errorf("lookup for ws-a with only ws-b registered: got %v, want Unknown", got)
	}
	if got := r.BridgePIDs("ws-a"); got != nil {
		t.Errorf("BridgePIDs(ws-a) = %v, want nil", got)
	}
}
