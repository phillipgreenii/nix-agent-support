// Package bridge tracks cmux-bridge registrations from the daemon's
// perspective so callers can distinguish "cmux", "cmux (bridge disconnected)"
// and "cmux (no bridge)" terminal states without requiring the daemon to be
// running inside cmux.
package bridge

import (
	"sort"
	"sync"
	"time"
)

// Status reports the daemon's view of a cmux-bridge for a given cmux
// workspace id.
type Status int

const (
	// Unknown: no bridge has ever registered for this cmux workspace.
	// Surfaced to users as "cmux (no bridge)".
	Unknown Status = iota
	// Alive: a bridge is registered and was seen within staleAfter.
	// Surfaced as "cmux".
	Alive
	// Stale: a bridge registered but has not refreshed within staleAfter.
	// Surfaced as "cmux (bridge disconnected)".
	Stale
)

// BridgeEntry is a single live-bridge set member for a cmux workspace. Multiple
// bridge processes may register against the same cmux workspace id (e.g.
// across bridge restarts or concurrent connections); each gets its own
// entry keyed by its own PID.
type BridgeEntry struct {
	BridgePID int
	// ServerPID is the cmux server pid the bridge reported; informational only
	// (the registry is keyed by workspace id). Zero when the bridge sent none.
	ServerPID int
	lastSeen  time.Time
	// gen identifies the stream that last attached this member, so a
	// superseded stream's teardown cannot touch a newer stream's registration.
	gen uint64
	// disconnected is set when the owning stream ended while the bridge
	// process may still be alive; it reads as Stale until a stream re-attaches.
	disconnected bool
}

// Registry tracks cmux-bridge registrations keyed by cmux workspace id. Each
// workspace id maps to a set of bridge entries (keyed by bridge PID) so that
// multiple live bridges for the same cmux workspace can be tracked
// independently. Thread-safe.
type Registry struct {
	mu          sync.RWMutex
	byWorkspace map[string]map[int]*BridgeEntry
	staleAfter  time.Duration
	now         func() time.Time
	nextGen     uint64
}

// NewRegistry constructs a registry. staleAfter is the cutoff beyond which
// a registered bridge is reported as Stale.
func NewRegistry(staleAfter time.Duration) *Registry {
	return &Registry{
		byWorkspace: map[string]map[int]*BridgeEntry{},
		staleAfter:  staleAfter,
		now:         time.Now,
	}
}

// AttachStream adds or updates a live-bridge set member, refreshing its
// lastSeen to now.
func (r *Registry) AttachStream(workspaceID string, serverPID, bridgePID int) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attachLocked(workspaceID, serverPID, bridgePID, r.now())
}

// DetachStream records that the stream identified by gen (the value
// AttachStream returned) has ended. The member is kept but reads as Stale
// ("cmux (bridge disconnected)") until a stream re-attaches or the reaper
// finds the bridge process dead. It is a no-op when a newer stream has since
// re-attached the same (workspace, bridge pid): a reconnecting bridge keeps
// its pid, so the old stream's late teardown must not erase the new
// registration (which, with Heartbeat a no-op for a missing member, left the
// workspace reading "no bridge" for good).
func (r *Registry) DetachStream(workspaceID string, bridgePID int, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byWorkspace[workspaceID][bridgePID]; ok && e.gen == gen {
		e.disconnected = true
	}
}

// attachLocked inserts or refreshes a member. Callers must hold r.mu.
func (r *Registry) attachLocked(workspaceID string, serverPID, bridgePID int, at time.Time) uint64 {
	members := r.byWorkspace[workspaceID]
	if members == nil {
		members = map[int]*BridgeEntry{}
		r.byWorkspace[workspaceID] = members
	}
	e, ok := members[bridgePID]
	if !ok {
		e = &BridgeEntry{BridgePID: bridgePID}
		members[bridgePID] = e
	}
	e.ServerPID = serverPID
	e.lastSeen = at
	r.nextGen++
	e.gen = r.nextGen
	e.disconnected = false
	return e.gen
}

// Heartbeat refreshes a member's lastSeen.
// A no-op if the member does not exist.
func (r *Registry) Heartbeat(workspaceID string, bridgePID int, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	members := r.byWorkspace[workspaceID]
	if members == nil {
		return
	}
	e, ok := members[bridgePID]
	if !ok {
		return
	}
	e.lastSeen = at
}

// Deregister removes a single bridge member for a workspace. A no-op if the
// workspace or member does not exist.
func (r *Registry) Deregister(workspaceID string, bridgePID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	members := r.byWorkspace[workspaceID]
	if members == nil {
		return
	}
	delete(members, bridgePID)
	if len(members) == 0 {
		delete(r.byWorkspace, workspaceID)
	}
}

// Prune removes every bridge member whose bridge PID is no longer alive
// according to isAlive. Workspaces left with no members are removed entirely.
func (r *Registry) Prune(isAlive func(pid int) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for workspaceID, members := range r.byWorkspace {
		for bridgePID := range members {
			if !isAlive(bridgePID) {
				delete(members, bridgePID)
			}
		}
		if len(members) == 0 {
			delete(r.byWorkspace, workspaceID)
		}
	}
}

// BridgePIDs returns the bridge PIDs currently registered for workspaceID, in
// ascending order (nil when there are none). It reports membership only, not
// freshness; StatusForWorkspace reports liveness.
func (r *Registry) BridgePIDs(workspaceID string) []int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	members := r.byWorkspace[workspaceID]
	if len(members) == 0 {
		return nil
	}
	pids := make([]int, 0, len(members))
	for pid := range members {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// SetNowForTest overrides the clock used by the registry. Whitebox test
// hook so external callers can simulate time without resorting to sleeps.
func (r *Registry) SetNowForTest(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// StatusForWorkspace reports the bridge's liveness for the given cmux workspace
// PID, computed over the freshest set member's lastSeen.
func (r *Registry) StatusForWorkspace(workspaceID string) Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	members := r.byWorkspace[workspaceID]
	if len(members) == 0 {
		return Unknown
	}
	var freshest time.Time
	connected := false
	for _, e := range members {
		if e.disconnected {
			continue
		}
		connected = true
		if e.lastSeen.After(freshest) {
			freshest = e.lastSeen
		}
	}
	if !connected || r.now().Sub(freshest) >= r.staleAfter {
		return Stale
	}
	return Alive
}
