package store

import (
	"database/sql"
	"fmt"
)

// Well-known meta keys, per the design doc's section 7.6 ("meta: schema
// version, last heartbeat, last run, last sweep"). schema_version is
// written by migrate (migrations.go); the other three are written by
// heartbeat and run — later packets in this docket.
const (
	MetaKeySchemaVersion = "schema_version"
	MetaKeyLastHeartbeat = "last_heartbeat"
	MetaKeyLastRun       = "last_run"
	MetaKeyLastSweep     = "last_sweep"
)

// SetMeta inserts or replaces the meta row for key.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.sql.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("store: set meta %q: %w", key, err)
	}
	return nil
}

// GetMeta returns the meta value for key, or found=false if no such key
// has been set.
func (s *Store) GetMeta(key string) (value string, found bool, err error) {
	row := s.sql.QueryRow(`SELECT value FROM meta WHERE key = ?`, key)
	if err := row.Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("store: get meta %q: %w", key, err)
	}
	return value, true, nil
}
