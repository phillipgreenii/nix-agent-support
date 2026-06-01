package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type ToggleStore struct{ db *sql.DB }

func NewToggleStore(db *sql.DB) *ToggleStore { return &ToggleStore{db: db} }

var _ store.ToggleStore = (*ToggleStore)(nil)

func (s *ToggleStore) Get(ctx context.Context, name string) (bool, bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM system_toggles WHERE name = ? AND deleted_at IS NULL",
		name).Scan(&v)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return v != 0, true, nil
}

func (s *ToggleStore) Set(ctx context.Context, name string, value bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO system_toggles (name, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, name, boolInt(value), formatTime(time.Now().UTC()))
	return err
}

func (s *ToggleStore) All(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, value FROM system_toggles WHERE deleted_at IS NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		var v int
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		out[name] = v != 0
	}
	return out, rows.Err()
}
