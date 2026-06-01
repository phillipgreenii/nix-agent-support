package store

import (
	"context"
	"time"
)

// Block is the persisted snapshot of one 5h cost window from ccusage.
type Block struct {
	ID                 int64 // surrogate; assigned on insert
	BlockID            string
	StartedAt          time.Time
	EndedAt            time.Time
	PlanCapUSD         float64
	TotalCostUSD       float64
	TotalTokens        uint64
	RateLimitResetsAt  *time.Time
	CapHitAt           *time.Time
	LastProcessedAt    time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

type BlockStore interface {
	Upsert(ctx context.Context, b Block) (int64, error)
	GetActive(ctx context.Context, now time.Time, fresh FreshnessWindow) (*Block, error)
	// MarkOrphansDeleted soft-deletes blocks where NOW() not in [started,ended]
	// AND no contributions reference them. Returns count.
	MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error)
	// MarkRevived clears deleted_at on blocks that now have contributions.
	MarkRevived(ctx context.Context) (int64, error)
	HardDelete(ctx context.Context, cutoff time.Time) (int64, error)
}
