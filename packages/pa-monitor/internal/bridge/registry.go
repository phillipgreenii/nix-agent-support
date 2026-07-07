// Package bridge tracks cmux-bridge registrations from the daemon's
// perspective so callers can distinguish "cmux", "cmux (bridge disconnected)"
// and "cmux (no bridge)" terminal states without requiring the daemon to be
// running inside cmux.
package bridge

import (
	"sync"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
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

// BridgeEntry is a single live-bridge set member for a cmux server. Multiple
// bridge processes may register against the same cmux server PID (e.g.
// across bridge restarts or concurrent connections); each gets its own
// entry keyed by its own PID.
//
// send is nil for display-only members created by the back-compat
// Register method — those exist only to keep StatusForServer's liveness
// contract working for callers that predate stream delivery.
type BridgeEntry struct {
	BridgePID int
	send      func(*pb.DaemonMsg) error
	lastSeen  time.Time
}

// Registry tracks cmux-bridge registrations keyed by cmux server PID. Each
// server PID maps to a set of bridge entries (keyed by bridge PID) so that
// multiple live bridges for the same cmux server can be tracked
// independently. Thread-safe.
type Registry struct {
	mu         sync.RWMutex
	byServer   map[int]map[int]*BridgeEntry
	staleAfter time.Duration
	now        func() time.Time
}

// NewRegistry constructs a registry. staleAfter is the cutoff beyond which
// a registered bridge is reported as Stale.
func NewRegistry(staleAfter time.Duration) *Registry {
	return &Registry{
		byServer:   map[int]map[int]*BridgeEntry{},
		staleAfter: staleAfter,
		now:        time.Now,
	}
}

// Register records or refreshes a display-only bridge entry. serverPID is
// the cmux server PID derived from the bridge's ancestry; the registry
// treats it as opaque and trusts the caller's derivation.
//
// This is the back-compat path used by callers that only report liveness
// without carrying a stream send hook (bridgePID 0, no send). It keeps
// StatusForServer's Alive/Stale/Unknown contract working for them.
func (r *Registry) Register(serverPID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attachLocked(serverPID, 0, nil, r.now())
}

// AttachStream adds or updates a live-bridge set member carrying a stream
// send hook, refreshing its lastSeen to now.
func (r *Registry) AttachStream(serverPID, bridgePID int, send func(*pb.DaemonMsg) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attachLocked(serverPID, bridgePID, send, r.now())
}

// attachLocked inserts or refreshes a member. Callers must hold r.mu.
func (r *Registry) attachLocked(serverPID, bridgePID int, send func(*pb.DaemonMsg) error, at time.Time) {
	members := r.byServer[serverPID]
	if members == nil {
		members = map[int]*BridgeEntry{}
		r.byServer[serverPID] = members
	}
	e, ok := members[bridgePID]
	if !ok {
		e = &BridgeEntry{BridgePID: bridgePID}
		members[bridgePID] = e
	}
	if send != nil {
		e.send = send
	}
	e.lastSeen = at
}

// Heartbeat refreshes a member's lastSeen without touching its send hook.
// A no-op if the member does not exist.
func (r *Registry) Heartbeat(serverPID, bridgePID int, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	members := r.byServer[serverPID]
	if members == nil {
		return
	}
	e, ok := members[bridgePID]
	if !ok {
		return
	}
	e.lastSeen = at
}

// Deregister removes a single bridge member for a server. A no-op if the
// server or member does not exist.
func (r *Registry) Deregister(serverPID, bridgePID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	members := r.byServer[serverPID]
	if members == nil {
		return
	}
	delete(members, bridgePID)
	if len(members) == 0 {
		delete(r.byServer, serverPID)
	}
}

// Prune removes every bridge member whose bridge PID is no longer alive
// according to isAlive. Servers left with no members are removed entirely.
// The bridgePID-0 sentinel used by Register's display-only members is
// exempt from this liveness check and is never pruned by Prune.
func (r *Registry) Prune(isAlive func(pid int) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for serverPID, members := range r.byServer {
		for bridgePID := range members {
			// bridgePID 0 denotes a display-only Register member with no
			// real process behind it; it is never subject to liveness
			// pruning by PID.
			if bridgePID == 0 {
				continue
			}
			if !isAlive(bridgePID) {
				delete(members, bridgePID)
			}
		}
		if len(members) == 0 {
			delete(r.byServer, serverPID)
		}
	}
}

// LiveBridge returns a set member for serverPID that carries a non-nil send
// hook, preferring the freshest such member. Returns false if no such
// member exists (including when the only members are display-only).
func (r *Registry) LiveBridge(serverPID int) (*BridgeEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	members := r.byServer[serverPID]
	if members == nil {
		return nil, false
	}
	var best *BridgeEntry
	for _, e := range members {
		if e.send == nil {
			continue
		}
		if best == nil || e.lastSeen.After(best.lastSeen) {
			best = e
		}
	}
	if best == nil {
		return nil, false
	}
	// Return a copy rather than the map-owned pointer: the original struct
	// is mutated in place under the write lock by attachLocked and
	// Heartbeat, and returning the live pointer would let a caller race
	// those writes after this read lock is released.
	best2 := *best
	return &best2, true
}

// SetNowForTest overrides the clock used by the registry. Whitebox test
// hook so external callers can simulate time without resorting to sleeps.
func (r *Registry) SetNowForTest(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// StatusForServer reports the bridge's liveness for the given cmux server
// PID, computed over the freshest set member's lastSeen.
func (r *Registry) StatusForServer(serverPID int) Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	members := r.byServer[serverPID]
	if len(members) == 0 {
		return Unknown
	}
	var freshest time.Time
	for _, e := range members {
		if e.lastSeen.After(freshest) {
			freshest = e.lastSeen
		}
	}
	if r.now().Sub(freshest) >= r.staleAfter {
		return Stale
	}
	return Alive
}
