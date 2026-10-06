package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/usagelimit"
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

// TestCapacity_withUsageLimitForcesFreeToZero: a hit usage window means "not right
// now" however many slots are open, and the limit is carried so a caller can say
// when the pool accepts work again. The occupancy fields stay truthful.
func TestCapacity_withUsageLimitForcesFreeToZero(t *testing.T) {
	l := &usagelimit.Limit{Window: usagelimit.FiveHour, UsedPct: 100, ResetsAt: time.Unix(20_000, 0)}
	got := Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}.WithUsageLimit(l)
	want := Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 0, UsageLimit: l}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// TestCapacity_withNilUsageLimitIsUnchanged: no limit hit leaves Free and the
// absent usage_limit key exactly as before.
func TestCapacity_withNilUsageLimitIsUnchanged(t *testing.T) {
	c := Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	if got := c.WithUsageLimit(nil); got != c {
		t.Fatalf("got %+v want %+v", got, c)
	}
}
