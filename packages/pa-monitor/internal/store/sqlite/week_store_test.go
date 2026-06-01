package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestWeekStore_UpsertReturnsID(t *testing.T) {
	db := openTestDB(t)
	ws := NewWeekStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := ws.Upsert(ctx, store.Week{
		WeekID:          "2026-W22",
		StartedAt:       now,
		EndedAt:         now.Add(7 * 24 * time.Hour),
		WeekCapUSD:      1000,
		TotalCostUSD:    50,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id <= 0 {
		t.Errorf("Upsert returned id %d, want > 0", id)
	}

	id2, err := ws.Upsert(ctx, store.Week{
		WeekID:          "2026-W22",
		StartedAt:       now,
		EndedAt:         now.Add(7 * 24 * time.Hour),
		WeekCapUSD:      1000,
		TotalCostUSD:    60,
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

func TestWeekStore_GetActive_TimeWindow(t *testing.T) {
	db := openTestDB(t)
	ws := NewWeekStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	_, _ = ws.Upsert(ctx, store.Week{
		WeekID:          "past",
		StartedAt:       now.Add(-14 * 24 * time.Hour),
		EndedAt:         now.Add(-7 * 24 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	id, err := ws.Upsert(ctx, store.Week{
		WeekID:          "current",
		StartedAt:       now.Add(-2 * 24 * time.Hour),
		EndedAt:         now.Add(5 * 24 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ws.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got == nil || got.ID != id || got.WeekID != "current" {
		t.Errorf("GetActive = %+v, want current id=%d", got, id)
	}
}

func TestWeekStore_GetActive_StaleReturnsNil(t *testing.T) {
	db := openTestDB(t)
	ws := NewWeekStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Minute)

	_, _ = ws.Upsert(ctx, store.Week{
		WeekID:          "stale",
		StartedAt:       now.Add(-2 * 24 * time.Hour),
		EndedAt:         now.Add(5 * 24 * time.Hour),
		LastProcessedAt: stale,
		UpdatedAt:       stale,
	})

	got, err := ws.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got != nil {
		t.Errorf("GetActive returned %+v for stale row, want nil", got)
	}
}
