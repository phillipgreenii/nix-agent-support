// Package bridge tracks cmux-bridge registrations from the daemon's
// perspective so callers can distinguish "cmux", "cmux (bridge disconnected)"
// and "cmux (no bridge)" terminal states without requiring the daemon to be
// running inside cmux.
package bridge

import (
	"sync"
	"time"
)

// Status reports the daemon's view of a cmux-bridge for a given cmux
// server PID.
type Status int

const (
	// Unknown: no bridge has ever registered for this cmux server.
	// Surfaced to users as "cmux (no bridge)".
	Unknown Status = iota
	// Alive: a bridge is registered and was seen within staleAfter.
	// Surfaced as "cmux".
	Alive
	// Stale: a bridge registered but has not refreshed within staleAfter.
	// Surfaced as "cmux (bridge disconnected)".
	Stale
)

// Registry tracks cmux-bridge registrations keyed by cmux server PID.
// Thread-safe.
type Registry struct {
	mu         sync.RWMutex
	byServer   map[int]*entry
	staleAfter time.Duration
	now        func() time.Time
}

type entry struct {
	workspaceID string
	bridgePID   int
	serverPID   int
	lastSeen    time.Time
}

// NewRegistry constructs a registry. staleAfter is the cutoff beyond which
// a registered bridge is reported as Stale.
func NewRegistry(staleAfter time.Duration) *Registry {
	return &Registry{
		byServer:   map[int]*entry{},
		staleAfter: staleAfter,
		now:        time.Now,
	}
}

// Register records or refreshes a bridge entry. serverPID must be the
// cmux server PID derived from bridgePID's ancestry; the registry treats
// it as opaque and trusts the caller's derivation.
func (r *Registry) Register(workspaceID string, bridgePID, serverPID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byServer[serverPID] = &entry{
		workspaceID: workspaceID,
		bridgePID:   bridgePID,
		serverPID:   serverPID,
		lastSeen:    r.now(),
	}
}

// SetNowForTest overrides the clock used by the registry. Whitebox test
// hook so external callers can simulate time without resorting to sleeps.
func (r *Registry) SetNowForTest(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// StatusForServer reports the bridge's liveness for the given cmux server PID.
func (r *Registry) StatusForServer(serverPID int) Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byServer[serverPID]
	if !ok {
		return Unknown
	}
	if r.now().Sub(e.lastSeen) >= r.staleAfter {
		return Stale
	}
	return Alive
}
