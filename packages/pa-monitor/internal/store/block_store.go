package store

import (
	"context"
	"time"
)

// Block is the persisted snapshot of one 5h cost window from ccusage.
type Block struct {
	ID                int64 // surrogate; assigned on insert
	BlockID           string
	StartedAt         time.Time
	EndedAt           time.Time
	PlanCapUSD        float64
	TotalCostUSD      float64
	TotalTokens       uint64
	RateLimitResetsAt *time.Time
	CapHitAt          *time.Time
	LastProcessedAt   time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time

	// Authoritative status-line rate_limits windows (ADR 0021 §6), captured
	// from Claude Code's status-line stdin JSON. These are account-global and
	// distinct from the daemon's pause concept (RateLimitResetsAt above).
	//
	// A nil pointer means "unknown / stale" — explicitly distinct from a value
	// of 0 (a real "unused" reading) and MUST never read back as 1970. Phase 0
	// observed SevenDay* absent on this account, so those may be long-lived
	// nil. No consumer reads these yet — Phase 1 is persistence + proto plumbing.
	FiveHourPct      *float64   // 5h window used_percentage, [0,100]; nil = unknown
	SevenDayPct      *float64   // 7d window used_percentage, [0,100]; nil = unknown
	SevenDayResetsAt *time.Time // 7d window reset instant; nil = unknown
	LimitsCapturedAt *time.Time // capture ts of the limits reading; nil = unknown
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
