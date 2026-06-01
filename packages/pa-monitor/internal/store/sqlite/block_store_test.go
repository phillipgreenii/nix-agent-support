package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestBlockStore_UpsertReturnsID(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := bs.Upsert(ctx, store.Block{
		BlockID:         "2026-06-01T15Z",
		StartedAt:       now,
		EndedAt:         now.Add(5 * time.Hour),
		PlanCapUSD:      100,
		TotalCostUSD:    25.50,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id <= 0 {
		t.Errorf("Upsert returned id %d, want > 0", id)
	}

	// Idempotent upsert returns the same id.
	id2, err := bs.Upsert(ctx, store.Block{
		BlockID:         "2026-06-01T15Z",
		StartedAt:       now,
		EndedAt:         now.Add(5 * time.Hour),
		PlanCapUSD:      100,
		TotalCostUSD:    30.00,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert (second): %v", err)
	}
	if id2 != id {
		t.Errorf("id changed across upserts: %d -> %d", id, id2)
	}
}

func TestBlockStore_GetActive_TimeWindow(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// past block
	_, err := bs.Upsert(ctx, store.Block{
		BlockID:         "past",
		StartedAt:       now.Add(-10 * time.Hour),
		EndedAt:         now.Add(-5 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert past: %v", err)
	}
	// current block
	id, err := bs.Upsert(ctx, store.Block{
		BlockID:         "current",
		StartedAt:       now.Add(-1 * time.Hour),
		EndedAt:         now.Add(4 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert current: %v", err)
	}

	got, err := bs.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got == nil {
		t.Fatal("GetActive returned nil")
	}
	if got.ID != id || got.BlockID != "current" {
		t.Errorf("GetActive returned %+v, want current id=%d", got, id)
	}
}

func TestBlockStore_GetActive_StaleReturnsNil(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Minute)

	if _, err := bs.Upsert(ctx, store.Block{
		BlockID:         "stale",
		StartedAt:       now.Add(-1 * time.Hour),
		EndedAt:         now.Add(4 * time.Hour),
		LastProcessedAt: stale,
		UpdatedAt:       stale,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := bs.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got != nil {
		t.Errorf("GetActive returned %+v for stale row, want nil", got)
	}
}
