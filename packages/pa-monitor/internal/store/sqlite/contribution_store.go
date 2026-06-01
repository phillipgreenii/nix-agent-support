package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type ContributionStore struct{ db *sql.DB }

func NewContributionStore(db *sql.DB) *ContributionStore { return &ContributionStore{db: db} }

var _ store.ContributionStore = (*ContributionStore)(nil)

func (s *ContributionStore) UpsertBlock(ctx context.Context, c store.Contribution) error {
	return s.upsert(ctx, c, "session_block_contributions", "block_id")
}

func (s *ContributionStore) UpsertWeek(ctx context.Context, c store.Contribution) error {
	return s.upsert(ctx, c, "session_week_contributions", "week_id")
}

func (s *ContributionStore) upsert(ctx context.Context, c store.Contribution, table, parentCol string) error {
	now := time.Now().UTC()
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO `+table+` (session_id, `+parentCol+`, cost_usd, tokens, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id, `+parentCol+`) DO UPDATE SET
			cost_usd = excluded.cost_usd,
			tokens = excluded.tokens,
			updated_at = excluded.updated_at
	`, c.SessionID, c.ParentID, c.CostUSD, c.Tokens, formatTime(c.UpdatedAt))
	return err
}
