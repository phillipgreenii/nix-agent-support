package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestContributionStore_UpsertBlock(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	bs := NewBlockStore(db)
	cs := NewContributionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := ss.Upsert(ctx, store.Session{SessionID: "sid-1", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("session upsert: %v", err)
	}
	var sessionID int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = 'sid-1'").Scan(&sessionID); err != nil {
		t.Fatalf("session id lookup: %v", err)
	}
	blockID, err := bs.Upsert(ctx, store.Block{BlockID: "blk-1", StartedAt: now, EndedAt: now.Add(time.Hour), LastProcessedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("block upsert: %v", err)
	}

	if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: sessionID, ParentID: blockID, CostUSD: 1.5, Tokens: 100, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertBlock first: %v", err)
	}
	// idempotent — second upsert overwrites
	if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: sessionID, ParentID: blockID, CostUSD: 3.0, Tokens: 200, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertBlock second: %v", err)
	}

	var cost float64
	var tokens uint64
	if err := db.QueryRowContext(ctx,
		"SELECT cost_usd, tokens FROM session_block_contributions WHERE session_id = ? AND block_id = ?",
		sessionID, blockID).Scan(&cost, &tokens); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cost != 3.0 || tokens != 200 {
		t.Errorf("contribution = (%v, %d), want (3.0, 200)", cost, tokens)
	}
}

func TestContributionStore_CascadeOnSessionDelete(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	bs := NewBlockStore(db)
	cs := NewContributionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := ss.Upsert(ctx, store.Session{SessionID: "sid-1", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("session: %v", err)
	}
	var sessionID int64
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = 'sid-1'").Scan(&sessionID)
	blockID, _ := bs.Upsert(ctx, store.Block{BlockID: "blk-1", StartedAt: now, EndedAt: now.Add(time.Hour), LastProcessedAt: now, UpdatedAt: now})
	if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: sessionID, ParentID: blockID, CostUSD: 1, Tokens: 1, UpdatedAt: now}); err != nil {
		t.Fatalf("contrib: %v", err)
	}

	// hard delete session
	if _, err := db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", sessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_block_contributions").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("contributions remaining after cascade = %d, want 0", n)
	}
}
