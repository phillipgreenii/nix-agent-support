package store

import (
	"database/sql"
	"fmt"
	"unicode/utf8"
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

// DeleteMeta removes the meta row for key. Deleting a key that was never
// set is not an error.
func (s *Store) DeleteMeta(key string) error {
	if _, err := s.sql.Exec(`DELETE FROM meta WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete meta %q: %w", key, err)
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

// ListMetaPrefix returns every meta row whose key starts with prefix, keyed
// by the full key. An empty result is an empty (non-nil) map, not an error.
// The prefix is matched literally (no LIKE wildcards), so a key containing
// "%" or "_" cannot widen the match.
func (s *Store) ListMetaPrefix(prefix string) (map[string]string, error) {
	rows, err := s.sql.Query(
		`SELECT key, value FROM meta WHERE substr(key, 1, ?) = ? ORDER BY key`,
		utf8.RuneCountInString(prefix), prefix,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list meta prefix %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: list meta prefix %q: %w", prefix, err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list meta prefix %q: %w", prefix, err)
	}
	return out, nil
}
