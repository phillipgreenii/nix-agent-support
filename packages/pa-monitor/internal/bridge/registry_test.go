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
