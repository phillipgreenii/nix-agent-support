package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type BlockStore struct{ db *sql.DB }

func NewBlockStore(db *sql.DB) *BlockStore { return &BlockStore{db: db} }

var _ store.BlockStore = (*BlockStore)(nil)

func (s *BlockStore) Upsert(ctx context.Context, b store.Block) (int64, error) {
	now := time.Now().UTC()
	if b.LastProcessedAt.IsZero() {
		b.LastProcessedAt = now
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = now
	}
	_, err := s.db.ExecContext(
		ctx, `
		INSERT INTO blocks (
			block_id, started_at, ended_at, plan_cap_usd, total_cost_usd, total_tokens,
			rate_limit_resets_at, cap_hit_at, last_processed_at, updated_at,
			five_hour_pct, seven_day_pct, seven_day_resets_at, limits_captured_at,
			five_hour_resets_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(block_id) DO UPDATE SET
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			plan_cap_usd = excluded.plan_cap_usd,
			total_cost_usd = excluded.total_cost_usd,
			total_tokens = COALESCE(NULLIF(excluded.total_tokens, 0), blocks.total_tokens),
			-- Daemon-pause usage window: last-write-wins, deliberately NOT the
			-- COALESCE-preserve policy the status-line columns below use. This
			-- column mirrors the live tree's WindowResetsAt aggregate, so a NULL
			-- write is the KNOWN fact "no session is paused" (not "unknown
			-- reading") and MUST clear a window that has since lifted. Preserving
			-- it would latch the block as paused for the rest of its 5h life.
			rate_limit_resets_at = excluded.rate_limit_resets_at,
			cap_hit_at = COALESCE(blocks.cap_hit_at, excluded.cap_hit_at),
			last_processed_at = excluded.last_processed_at,
			updated_at = excluded.updated_at,
			-- Status-line rate_limits windows: a fresh reading (non-NULL) wins;
			-- a NULL/unknown reading preserves the last known value rather than
			-- overwriting it with "unknown". NULL means unknown/stale, not 0.
			five_hour_pct = COALESCE(excluded.five_hour_pct, blocks.five_hour_pct),
			seven_day_pct = COALESCE(excluded.seven_day_pct, blocks.seven_day_pct),
			seven_day_resets_at = COALESCE(excluded.seven_day_resets_at, blocks.seven_day_resets_at),
			limits_captured_at = COALESCE(excluded.limits_captured_at, blocks.limits_captured_at),
			five_hour_resets_at = COALESCE(excluded.five_hour_resets_at, blocks.five_hour_resets_at),
			deleted_at = NULL
	`,
		b.BlockID, formatTime(b.StartedAt), formatTime(b.EndedAt),
		b.PlanCapUSD, b.TotalCostUSD, b.TotalTokens,
		timePtr(b.RateLimitResetsAt), timePtr(b.CapHitAt),
		formatTime(b.LastProcessedAt), formatTime(b.UpdatedAt),
		floatPtr(b.FiveHourPct), floatPtr(b.SevenDayPct),
		timePtr(b.SevenDayResetsAt), timePtr(b.LimitsCapturedAt),
		timePtr(b.FiveHourResetsAt),
	)
	if err != nil {
		return 0, err
	}
	// LastInsertId is unreliable on an ON CONFLICT update in modernc, so map
	// back to the actual row id by looking it up by block_id.
	var id int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM blocks WHERE block_id = ?", b.BlockID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *BlockStore) GetActive(ctx context.Context, now time.Time, fresh store.FreshnessWindow) (*store.Block, error) {
	cutoff := now.Add(-fresh.Blocks)
	row := s.db.QueryRowContext(ctx, blockSelectColumns+`
		FROM blocks
		WHERE deleted_at IS NULL
		  AND last_processed_at > ?
		  AND started_at <= ?
		  AND ended_at >= ?
		ORDER BY started_at DESC LIMIT 1
	`, formatTime(cutoff), formatTime(now), formatTime(now))
	return scanBlock(row)
}

func (s *BlockStore) MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE blocks SET deleted_at = ?
		WHERE deleted_at IS NULL
		  AND NOT (started_at <= ? AND ended_at >= ?)
		  AND id NOT IN (SELECT DISTINCT block_id FROM session_block_contributions)
	`, formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *BlockStore) MarkRevived(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE blocks SET deleted_at = NULL
		WHERE deleted_at IS NOT NULL
		  AND id IN (SELECT DISTINCT block_id FROM session_block_contributions)
	`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *BlockStore) HardDelete(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM blocks WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const blockSelectColumns = `SELECT
	id, block_id, started_at, ended_at, plan_cap_usd, total_cost_usd, total_tokens,
	rate_limit_resets_at, cap_hit_at, last_processed_at, updated_at, deleted_at,
	five_hour_pct, seven_day_pct, seven_day_resets_at, limits_captured_at,
	five_hour_resets_at`

func scanBlock(r rowScanner) (*store.Block, error) {
	var (
		b                store.Block
		rateResetsAt     sql.NullString
		capHitAt         sql.NullString
		deletedAt        sql.NullString
		startedAt, ended string
		processed, upd   string
		fiveHourPct      sql.NullFloat64
		sevenDayPct      sql.NullFloat64
		sevenDayResetsAt sql.NullString
		limitsCapturedAt sql.NullString
		fiveHourResetsAt sql.NullString
	)
	err := r.Scan(
		&b.ID, &b.BlockID, &startedAt, &ended, &b.PlanCapUSD, &b.TotalCostUSD, &b.TotalTokens,
		&rateResetsAt, &capHitAt, &processed, &upd, &deletedAt,
		&fiveHourPct, &sevenDayPct, &sevenDayResetsAt, &limitsCapturedAt,
		&fiveHourResetsAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.StartedAt = parseTime(startedAt)
	b.EndedAt = parseTime(ended)
	b.LastProcessedAt = parseTime(processed)
	b.UpdatedAt = parseTime(upd)
	if rateResetsAt.Valid {
		t := parseTime(rateResetsAt.String)
		b.RateLimitResetsAt = &t
	}
	if capHitAt.Valid {
		t := parseTime(capHitAt.String)
		b.CapHitAt = &t
	}
	if deletedAt.Valid {
		t := parseTime(deletedAt.String)
		b.DeletedAt = &t
	}
	// Status-line rate_limits: a NULL column MUST stay nil (unknown/stale),
	// never decode to 0 or a 1970 timestamp.
	if fiveHourPct.Valid {
		v := fiveHourPct.Float64
		b.FiveHourPct = &v
	}
	if sevenDayPct.Valid {
		v := sevenDayPct.Float64
		b.SevenDayPct = &v
	}
	if sevenDayResetsAt.Valid {
		t := parseTime(sevenDayResetsAt.String)
		b.SevenDayResetsAt = &t
	}
	if limitsCapturedAt.Valid {
		t := parseTime(limitsCapturedAt.String)
		b.LimitsCapturedAt = &t
	}
	if fiveHourResetsAt.Valid {
		t := parseTime(fiveHourResetsAt.String)
		b.FiveHourResetsAt = &t
	}
	return &b, nil
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func floatPtr(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}
