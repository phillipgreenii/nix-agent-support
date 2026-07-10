package store

import (
	"context"
	"time"
)

// Session is the persisted snapshot of one Claude Code session.
// Mirrors the spec's `sessions` table 1-to-1 (except surrogate id, which
// is internal to the SQLite impl).
type Session struct {
	SessionID    string
	PID          *int // nil when process is dead
	CommandHash  string
	Cwd          string
	Name         string
	Kind         string
	Entrypoint   string
	Model        string
	TerminalHost string
	Branch       string
	Status       string
	// Blocker is the ADR 0024 blocker string ("human_input" | "human_authn" |
	// "usage_limit" | "error"); empty when Status != "blocked". Persisted so the
	// DB-path bucketer can render usage_limit even though the DB does not store
	// RateLimitResetsAt (R9).
	Blocker               string
	FirstPrompt           string
	Labels                map[string]string
	TranscriptMTime       time.Time
	StartedAt             time.Time
	ContextTokens         uint64
	SessionTokens         uint64
	SubagentCount         uint32
	SubshellCount         uint32
	BurnRateShort         float64
	BurnRateLong          float64
	CostUSD               float64
	AwaitingInput         bool
	LastErrorKind         string
	LastErrorText         string
	LastErrorAt           time.Time
	LastErrorTerminal     bool
	LastErrorRetryable    bool
	LastErrorFromSubagent bool
	LastProcessedAt       time.Time
	UpdatedAt             time.Time
	CreatedAt             time.Time
	DeletedAt             *time.Time
}

// SessionStore is the persistence interface for sessions.
type SessionStore interface {
	// Upsert inserts or updates by SessionID. CreatedAt is set on insert.
	// LastProcessedAt is always set to now. UpdatedAt is set to now only when
	// other fields actually changed (implementation detail of the impl).
	Upsert(ctx context.Context, s Session) error

	// List returns sessions matching the filter, plus the freshness window
	// gate. activeBlockID is the surrogate id (not the string block_id) used
	// for joining contributions.
	List(ctx context.Context, filter Filter, activeBlockID int64, fresh FreshnessWindow) ([]SessionWithContribution, error)

	// GetByID returns one session by its SessionID. Returns nil if absent
	// (deleted or stale).
	GetByID(ctx context.Context, sessionID string, fresh FreshnessWindow) (*Session, error)

	// MarkDeleted sets deleted_at on sessions whose SessionID is NOT in
	// keepIDs. Called by GC after listing the filesystem.
	MarkDeleted(ctx context.Context, keepIDs []string, now time.Time) error

	// MarkRevived clears deleted_at on sessions whose SessionID IS in
	// reviveIDs. Called by GC when files reappear.
	MarkRevived(ctx context.Context, reviveIDs []string) error

	// HardDelete removes rows soft-deleted before cutoff. Returns count.
	// Cascades to contributions and nudge history.
	HardDelete(ctx context.Context, cutoff time.Time) (int64, error)

	// AllSessionIDs returns every SessionID present (alive or soft-deleted).
	// Used by GC's file-reconciliation step.
	AllSessionIDs(ctx context.Context) ([]string, error)

	// MarkEscalated sets last_error_retryable = false for the given session,
	// persisting the escalation flip that the daemon's escalation loop applies
	// to the in-memory tree. No-op if the session does not exist.
	MarkEscalated(ctx context.Context, sessionID string) error
}

// SessionWithContribution joins a session with its contribution to a
// specific block. block_cost/tokens are zero when there is no contribution.
type SessionWithContribution struct {
	Session
	BlockCostUSD float64
	BlockTokens  uint64
}
