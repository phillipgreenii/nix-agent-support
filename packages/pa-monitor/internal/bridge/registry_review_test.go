package bridge

import (
	"testing"
	"time"
)

// TestRegistry_StatusForWorkspaceStaleAtExactBoundary pins the cutoff direction:
// a member whose age is EXACTLY staleAfter is stale (the `>=` boundary). A
// regression flipping `>=` to `>` would be caught here.
func TestRegistry_StatusForWorkspaceStaleAtExactBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cur := now
	r := NewRegistry(30 * time.Second)
	r.now = func() time.Time { return cur }
	r.AttachStream("ws-4000", 4000, 100)

	// Exactly staleAfter (30s): age == staleAfter must be treated as stale.
	cur = now.Add(30 * time.Second)
	if got := r.StatusForWorkspace("ws-4000"); got != Stale {
		t.Errorf("StatusForWorkspace(ws-4000) at exactly staleAfter (30s): got %v, want Stale (>= cutoff)", got)
	}
}
