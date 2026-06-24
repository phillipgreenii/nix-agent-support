package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SetMeta upserts value for (externalID, key). An empty key is an error; an
// empty value is allowed (a bare tag). Replaces any existing value for the key.
func (s *Store) SetMeta(ctx context.Context, externalID, key, value string) error {
	if key == "" {
		return fmt.Errorf("set meta: key is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_metadata (external_id, key, value) VALUES (?,?,?)
		 ON CONFLICT(external_id, key) DO UPDATE SET value = excluded.value`,
		externalID, key, value)
	if err != nil {
		return fmt.Errorf("set meta %q/%q: %w", externalID, key, err)
	}
	return nil
}

// GetMeta returns the value for (externalID, key). ok=false (no error) when the
// key is not set for that session.
func (s *Store) GetMeta(ctx context.Context, externalID, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM session_metadata WHERE external_id = ? AND key = ?`,
		externalID, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get meta %q/%q: %w", externalID, key, err)
	}
	return v, true, nil
}

// Meta returns all metadata for externalID as a map (non-nil empty map when
// none).
func (s *Store) Meta(ctx context.Context, externalID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM session_metadata WHERE external_id = ?`, externalID)
	if err != nil {
		return nil, fmt.Errorf("meta %q: %w", externalID, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// DeleteMeta removes (externalID, key). Removing an absent key is not an error.
func (s *Store) DeleteMeta(ctx context.Context, externalID, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM session_metadata WHERE external_id = ? AND key = ?`, externalID, key)
	if err != nil {
		return fmt.Errorf("delete meta %q/%q: %w", externalID, key, err)
	}
	return nil
}
