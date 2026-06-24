package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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

// ListExternalIDsByMeta returns external_ids whose metadata matches ALL filters
// (AND across keys; exact value match per key), sorted ascending. An empty
// filters map returns every external_id that has ANY metadata row (distinct).
func (s *Store) ListExternalIDsByMeta(ctx context.Context, filters map[string]string) ([]string, error) {
	if len(filters) == 0 {
		return s.scanExternalIDs(ctx,
			`SELECT DISTINCT external_id FROM session_metadata ORDER BY external_id ASC`)
	}
	// Build "(key,value) IN ((?,?),(?,?),...)" then require a row per filter via
	// HAVING COUNT(*) = len(filters). PRIMARY KEY(external_id,key) guarantees a
	// session contributes at most one matching row per key, so the count is exact.
	placeholders := ""
	args := make([]any, 0, len(filters)*2+1)
	i := 0
	for k, v := range filters {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "(?,?)"
		args = append(args, k, v)
		i++
	}
	args = append(args, len(filters))
	q := `SELECT external_id FROM session_metadata
	      WHERE (key, value) IN (` + placeholders + `)
	      GROUP BY external_id
	      HAVING COUNT(*) = ?
	      ORDER BY external_id ASC`
	return s.scanExternalIDs(ctx, q, args...)
}

// scanExternalIDs runs q and collects the single-column external_id result.
func (s *Store) scanExternalIDs(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list external_ids by meta: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out) // ORDER BY already sorts; belt-and-suspenders for determinism
	return out, nil
}
