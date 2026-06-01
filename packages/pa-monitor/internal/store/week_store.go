package store

import (
	"context"
	"time"
)

// Week is the persisted snapshot of one weekly cost window.
type Week struct {
	ID              int64
	WeekID          string
	StartedAt       time.Time
	EndedAt         time.Time
	WeekCapUSD      float64
	TotalCostUSD    float64
	CapHitAt        *time.Time
	LastProcessedAt time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

type WeekStore interface {
	Upsert(ctx context.Context, w Week) (int64, error)
	GetActive(ctx context.Context, now time.Time, fresh FreshnessWindow) (*Week, error)
	MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error)
	MarkRevived(ctx context.Context) (int64, error)
	HardDelete(ctx context.Context, cutoff time.Time) (int64, error)
}
