package bridge

import (
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// TestRegistry_LiveBridgeSelectsFreshOverStaleMember guards that when a server
// has BOTH a stale and a fresh send-carrying member, LiveBridge returns the
// FRESH one and never the stale one — the stale member is skipped even though
// its send hook is non-nil. The single-member TestRegistry_LiveBridgeSkipsStaleMember
// proves a lone stale member is dropped; this pins that a coexisting stale
// member cannot win selection over a fresh one. (pg2-gweng, from the pg2-4tkw review.)
func TestRegistry_LiveBridgeSelectsFreshOverStaleMember(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cur := now
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return cur }

	noop := func(*pb.DaemonMsg) error { return nil }
	// Member 100 attaches first; member 200 attaches 20s later. Both non-nil send.
	r.AttachStream(4000, 100, noop)
	cur = now.Add(20 * time.Second)
	r.AttachStream(4000, 200, noop)

	// At now+35s: member 100 is 35s old (stale, >30s), member 200 is 15s old
	// (fresh). LiveBridge must return 200 even though 100's send hook is alive.
	cur = now.Add(35 * time.Second)
	live, ok := r.LiveBridge(4000)
	if !ok {
		t.Fatal("LiveBridge(4000): got !ok, want the fresh member 200")
	}
	if live.BridgePID != 200 {
		t.Errorf("LiveBridge(4000).BridgePID = %d, want 200 (fresh); a stale send-carrying member must not win", live.BridgePID)
	}
}

// TestRegistry_LiveBridgeExcludesAtExactStaleBoundary pins the cutoff direction:
// a member whose age is EXACTLY staleAfter is stale (the `>=` boundary), matching
// StatusForServer. A regression flipping `>=` to `>` would be caught here.
// (pg2-gweng, from the pg2-4tkw review.)
func TestRegistry_LiveBridgeExcludesAtExactStaleBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cur := now
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return cur }
	r.AttachStream(4000, 100, func(*pb.DaemonMsg) error { return nil })

	// Exactly staleAfter (30s): age == staleAfter must be treated as stale.
	cur = now.Add(30 * time.Second)
	if _, ok := r.LiveBridge(4000); ok {
		t.Error("LiveBridge(4000) at exactly staleAfter (30s): got ok, want !ok (>= cutoff)")
	}
}
