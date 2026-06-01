package store

import (
	"context"
	"time"
)

// Contribution is the per-session contribution to one block or one week.
// SessionID and ParentID are surrogate row IDs from sessions and blocks
// (or weeks); the store's UpsertBlock / UpsertWeek pick which parent table.
type Contribution struct {
	SessionID int64
	ParentID  int64
	CostUSD   float64
	Tokens    uint64
	UpdatedAt time.Time
}

type ContributionStore interface {
	UpsertBlock(ctx context.Context, c Contribution) error
	UpsertWeek(ctx context.Context, c Contribution) error
}
