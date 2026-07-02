package store

import (
	"context"
	"time"
)

// NudgeEvent is one immutable row from nudge_history.
// Sources is the unsorted set of contributing sources joined from
// nudge_history_sources.
type NudgeEvent struct {
	SessionID       int64
	Text            string
	Result          string // 'sent' | 'failed' | 'suppressed' | 'escalated'
	ErrorText       string
	CausedByErrorAt *time.Time
	Escalated       bool
	FiredAt         time.Time
	Sources         []string
}

type NudgeStore interface {
	// Record inserts a nudge_history row plus its sources rows in one tx.
	Record(ctx context.Context, ev NudgeEvent) error

	// LatestForSession returns the most recent NudgeEvent for the session,
	// or nil if absent.
	LatestForSession(ctx context.Context, sessionID int64) (*NudgeEvent, error)

	// LatestForSessionWithSource returns the most recent NudgeEvent for the
	// session that carries the given source. Used for disrupt-cooldown checks.
	LatestForSessionWithSource(ctx context.Context, sessionID int64, source string) (*NudgeEvent, error)
}
