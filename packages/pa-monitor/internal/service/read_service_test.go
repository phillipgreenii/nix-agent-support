package service

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

func TestReadService_GetState_AllFilter(t *testing.T) {
	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	pid := 100

	// Insert a block.
	bs := sqlite.NewBlockStore(db)
	blockID, _ := bs.Upsert(ctx, store.Block{
		BlockID: "blk", StartedAt: now.Add(-time.Hour), EndedAt: now.Add(time.Hour),
		LastProcessedAt: now, UpdatedAt: now,
	})

	ss := sqlite.NewSessionStore(db)
	cs := sqlite.NewContributionStore(db)
	// alive session
	_ = ss.Upsert(ctx, store.Session{SessionID: "alive", PID: &pid, Cwd: "/a", Status: "working", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})
	// dead-PID but contributing
	_ = ss.Upsert(ctx, store.Session{SessionID: "dead-contrib", Cwd: "/b", Status: "idle", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})
	var aliveID, deadID int64
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id='alive'").Scan(&aliveID)
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id='dead-contrib'").Scan(&deadID)
	_ = cs.UpsertBlock(ctx, store.Contribution{SessionID: aliveID, ParentID: blockID, CostUSD: 1, Tokens: 10, UpdatedAt: now})
	_ = cs.UpsertBlock(ctx, store.Contribution{SessionID: deadID, ParentID: blockID, CostUSD: 2, Tokens: 20, UpdatedAt: now})
	// dead-PID, no contribution (should NOT appear in either filter)
	_ = ss.Upsert(ctx, store.Session{SessionID: "ghost", Cwd: "/c", Status: "dormant", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})

	rs := NewReadService(ReadDeps{
		Sessions: ss, Blocks: bs, Weeks: sqlite.NewWeekStore(db), Toggles: sqlite.NewToggleStore(db), Nudges: sqlite.NewNudgeStore(db),
	})

	all, err := rs.GetState(ctx, store.FilterAll)
	if err != nil {
		t.Fatalf("GetState all: %v", err)
	}
	if len(all.Sessions) != 2 {
		t.Errorf("All filter returned %d sessions, want 2 (alive + dead-contrib)", len(all.Sessions))
	}

	active, err := rs.GetState(ctx, store.FilterActive)
	if err != nil {
		t.Fatalf("GetState active: %v", err)
	}
	if len(active.Sessions) != 1 {
		t.Errorf("Active filter returned %d sessions, want 1 (alive only)", len(active.Sessions))
	}
}

func TestReadService_GetState_NoActiveBlock(t *testing.T) {
	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	pid := 100

	ss := sqlite.NewSessionStore(db)
	// Alive session, no contributions, no active block in the DB.
	_ = ss.Upsert(ctx, store.Session{
		SessionID:       "alive-no-block",
		PID:             &pid,
		Cwd:             "/x",
		Status:          "working",
		LastProcessedAt: now,
		UpdatedAt:       now,
		CreatedAt:       now,
	})

	rs := NewReadService(ReadDeps{
		Sessions: ss,
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})

	st, err := rs.GetState(ctx, store.FilterAll)
	if err != nil {
		t.Fatalf("GetState all: %v", err)
	}
	if st.Block != nil {
		t.Errorf("Block = %+v, want nil (no active block)", st.Block)
	}
	if len(st.Sessions) != 1 {
		t.Errorf("FilterAll w/ no block returned %d sessions, want 1 (alive PID still counts)", len(st.Sessions))
	}

	st, err = rs.GetState(ctx, store.FilterActive)
	if err != nil {
		t.Fatalf("GetState active: %v", err)
	}
	if len(st.Sessions) != 0 {
		t.Errorf("FilterActive w/ no block returned %d sessions, want 0 (active requires block)", len(st.Sessions))
	}
}
