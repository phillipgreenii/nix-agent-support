package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// TestCapacity_countsOnlyNonPreservedLiveRows: a dead (not tmux-live) row must
// not count as live at all, and a live needs_input row must be excluded from
// Counted (ADR 0072, Decision 1) while still contributing to Live/Preserved.
func TestCapacity_countsOnlyNonPreservedLiveRows(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, _ := reapFixtureStates(t, now,
		map[string]int64{"paused": 100, "work": 200, "idle": 300},
		map[string]store.State{"paused": store.NeedsInput, "work": store.Working, "idle": store.Idle})
	// a dead row must not count as live: insert one without a tmux session
	if err := s.d.Store.Insert(context.Background(), store.Session{ExternalID: "dead", ClaudeSessionID: "csid-dead", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Capacity(context.Background(), 6)
	if err != nil {
		t.Fatal(err)
	}
	want := Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	if c != want {
		t.Fatalf("got %+v want %+v", c, want)
	}
}

// TestCapacity_freeFloorsAtZero: Free must never go negative when Counted
// exceeds MaxSessions.
func TestCapacity_freeFloorsAtZero(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, _ := reapFixtureStates(t, now,
		map[string]int64{"w0": 1, "w1": 2, "w2": 3},
		map[string]store.State{"w0": store.Working, "w1": store.Working, "w2": store.Working})
	c, err := s.Capacity(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Free != 0 || c.Counted != 3 {
		t.Fatalf("got %+v", c)
	}
}
