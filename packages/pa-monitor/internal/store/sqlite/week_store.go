package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type WeekStore struct{ db *sql.DB }

func NewWeekStore(db *sql.DB) *WeekStore { return &WeekStore{db: db} }

var _ store.WeekStore = (*WeekStore)(nil)

func (s *WeekStore) Upsert(ctx context.Context, w store.Week) (int64, error) {
	now := time.Now().UTC()
	if w.LastProcessedAt.IsZero() {
		w.LastProcessedAt = now
	}
	if w.UpdatedAt.IsZero() {
		w.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO weeks (
			week_id, started_at, ended_at, week_cap_usd, total_cost_usd,
			cap_hit_at, last_processed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(week_id) DO UPDATE SET
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			week_cap_usd = excluded.week_cap_usd,
			total_cost_usd = excluded.total_cost_usd,
			cap_hit_at = COALESCE(weeks.cap_hit_at, excluded.cap_hit_at),
			last_processed_at = excluded.last_processed_at,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, w.WeekID, formatTime(w.StartedAt), formatTime(w.EndedAt),
		w.WeekCapUSD, w.TotalCostUSD, timePtr(w.CapHitAt),
		formatTime(w.LastProcessedAt), formatTime(w.UpdatedAt))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM weeks WHERE week_id = ?", w.WeekID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *WeekStore) GetActive(ctx context.Context, now time.Time, fresh store.FreshnessWindow) (*store.Week, error) {
	cutoff := now.Add(-fresh.Weeks)
	row := s.db.QueryRowContext(ctx, weekSelectColumns+`
		FROM weeks
		WHERE deleted_at IS NULL
		  AND last_processed_at > ?
		  AND started_at <= ?
		  AND ended_at >= ?
		ORDER BY started_at DESC LIMIT 1
	`, formatTime(cutoff), formatTime(now), formatTime(now))
	return scanWeek(row)
}

func (s *WeekStore) MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE weeks SET deleted_at = ?
		WHERE deleted_at IS NULL
		  AND NOT (started_at <= ? AND ended_at >= ?)
		  AND id NOT IN (SELECT DISTINCT week_id FROM session_week_contributions)
	`, formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *WeekStore) MarkRevived(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE weeks SET deleted_at = NULL
		WHERE deleted_at IS NOT NULL
		  AND id IN (SELECT DISTINCT week_id FROM session_week_contributions)
	`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *WeekStore) HardDelete(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM weeks WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const weekSelectColumns = `SELECT
	id, week_id, started_at, ended_at, week_cap_usd, total_cost_usd,
	cap_hit_at, last_processed_at, updated_at, deleted_at`

func scanWeek(r rowScanner) (*store.Week, error) {
	var (
		w                store.Week
		capHitAt         sql.NullString
		deletedAt        sql.NullString
		startedAt, ended string
		processed, upd   string
	)
	err := r.Scan(
		&w.ID, &w.WeekID, &startedAt, &ended, &w.WeekCapUSD, &w.TotalCostUSD,
		&capHitAt, &processed, &upd, &deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.StartedAt = parseTime(startedAt)
	w.EndedAt = parseTime(ended)
	w.LastProcessedAt = parseTime(processed)
	w.UpdatedAt = parseTime(upd)
	if capHitAt.Valid {
		t := parseTime(capHitAt.String)
		w.CapHitAt = &t
	}
	if deletedAt.Valid {
		t := parseTime(deletedAt.String)
		w.DeletedAt = &t
	}
	return &w, nil
}
