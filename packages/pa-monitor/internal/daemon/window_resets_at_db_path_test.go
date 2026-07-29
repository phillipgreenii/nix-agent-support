package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// windowPathHarness is the full served-surface path for the daemon-pause usage
// window: aggregate.Build (live tree) -> blockToStoreBlockWithLimits -> sqlite
// upsert -> ReadService.GetState -> convertStateToAggregateTree -> proto.FromTree.
// It returns the DaemonState every external consumer is served (the TUI paused
// state, the "resuming in N:NN" banner, the cmux sidebar all read
// window_resets_at off this message).
type windowPathHarness struct {
	blocks *sqlite.BlockStore
	rs     *service.ReadService
	now    time.Time
	block  *usage.Block
}

func newWindowPathHarness(t *testing.T, now time.Time) *windowPathHarness {
	t.Helper()

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	sessions := sqlite.NewSessionStore(db)
	blocks := sqlite.NewBlockStore(db)
	rs := service.NewReadService(service.ReadDeps{
		Sessions: sessions,
		Blocks:   blocks,
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})
	rs.SetClock(func() time.Time { return now })

	// One session, blocked on a usage limit — the shape a rate-limited session
	// takes on both paths (ADR 0024: status=blocked, blocker=usage_limit).
	pid := 4242
	if err := sessions.Upsert(context.Background(), store.Session{
		SessionID:       "paused-sess",
		PID:             &pid, // non-nil so FilterAll returns the row
		Cwd:             "/repo",
		Status:          session.Blocked.String(),
		Blocker:         session.UsageLimit.String(),
		LastProcessedAt: now,
		UpdatedAt:       now,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("session Upsert: %v", err)
	}

	return &windowPathHarness{
		blocks: blocks,
		rs:     rs,
		now:    now,
		block: &usage.Block{
			ID:        "block-1",
			StartTime: now.Add(-1 * time.Hour),
			EndTime:   now.Add(4 * time.Hour),
			IsActive:  true,
			CostUSD:   12.5,
		},
	}
}

// liveTree builds the tree the poller publishes: one session whose enrichment
// carries resetsAt, so aggregate.Build sets Tree.WindowResetsAt (a zero resetsAt
// models the window having lifted).
func (h *windowPathHarness) liveTree(resetsAt time.Time) *aggregate.Tree {
	sess := &session.Session{
		SessionID: "paused-sess",
		Cwd:       "/repo",
		Status:    session.Blocked,
		Blocker:   session.UsageLimit,
	}
	enriched := map[string]aggregate.SessionEnrichment{
		"paused-sess": {RateLimitResetsAt: resetsAt},
	}
	return aggregate.Build([]*session.Session{sess}, enriched, nil, h.block, 100)
}

// serve persists the live tree's block and reads the served DaemonState back out
// through every layer in between.
func (h *windowPathHarness) serve(t *testing.T, live *aggregate.Tree) *pb.DaemonState {
	t.Helper()

	sb := blockToStoreBlockWithLimits(live.ActiveBlock, 100, h.now, live)
	if _, err := h.blocks.Upsert(context.Background(), sb); err != nil {
		t.Fatalf("block Upsert: %v", err)
	}

	st, err := h.rs.GetState(context.Background(), store.FilterAll)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st == nil {
		t.Fatal("GetState returned nil state")
	}
	if st.Block == nil {
		t.Fatal("GetState returned no active block; the fixture block must be active+fresh")
	}

	dbTree := convertStateToAggregateTree(st)
	if dbTree == nil {
		t.Fatal("convertStateToAggregateTree returned nil")
	}
	return pb.FromTree(dbTree)
}

// TestServedWindowResetsAt_SurvivesStoreRoundTrip is the regression test for
// pg2-tdzkq: nothing populated store.Block.RateLimitResetsAt, so the served
// DaemonState.window_resets_at was permanently unset and every operator-facing
// surface reported no usage window even while a session was paused on a rate
// limit. The served value MUST be non-zero whenever the live tree's is.
func TestServedWindowResetsAt_SurvivesStoreRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	resetsAt := now.Add(37 * time.Minute)

	h := newWindowPathHarness(t, now)

	live := h.liveTree(resetsAt)
	if !live.WindowResetsAt.Equal(resetsAt) {
		t.Fatalf("precondition: live tree WindowResetsAt = %v, want %v", live.WindowResetsAt, resetsAt)
	}

	got := h.serve(t, live)

	ts := got.GetWindowResetsAt()
	if ts == nil {
		t.Fatal("served DaemonState.window_resets_at is unset; the live tree's window was lost on the store -> GetState -> convert -> proto path")
	}
	if !ts.AsTime().Equal(resetsAt) {
		t.Errorf("served window_resets_at = %v, want %v", ts.AsTime(), resetsAt)
	}
}

// TestServedWindowResetsAt_ClearsWhenWindowLifts guards the merge policy for the
// blocks.rate_limit_resets_at column. The column mirrors the live tree's
// WindowResetsAt aggregate, so a NULL write is KNOWN "no session is paused" and
// MUST clear a previously-persisted window. A COALESCE-preserve policy (the one
// the status-line columns use, where NULL means "unknown reading") would latch
// the block as paused for the rest of its 5h life and leave the TUI/cmux
// permanently showing a stale pause.
func TestServedWindowResetsAt_ClearsWhenWindowLifts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	resetsAt := now.Add(37 * time.Minute)

	h := newWindowPathHarness(t, now)

	// Tick 1: paused.
	if got := h.serve(t, h.liveTree(resetsAt)); got.GetWindowResetsAt() == nil {
		t.Fatal("precondition: served window_resets_at unset while paused")
	}

	// Tick 2: the window has lifted — the live aggregate is zero again.
	lifted := h.liveTree(time.Time{})
	if !lifted.WindowResetsAt.IsZero() {
		t.Fatalf("precondition: lifted tree WindowResetsAt = %v, want zero", lifted.WindowResetsAt)
	}

	got := h.serve(t, lifted)
	if ts := got.GetWindowResetsAt(); ts != nil {
		t.Errorf("served window_resets_at = %v after the window lifted, want unset (stale pause latched)", ts.AsTime())
	}
}
