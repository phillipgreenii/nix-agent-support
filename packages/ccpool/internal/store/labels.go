package store

import (
	"context"
	"fmt"
)

// MarkAsLabel flags an existing session_metadata key as label-eligible: one
// boolean column (is_label) on the existing table, no new table
// (pg2-24f89/pg2-qye99 D8.1). Marking a key that was never written via SetMeta
// is an error (key not found), not a silent no-op — a caller must not be able
// to believe a typo'd key is now label-eligible when nothing is set behind it.
//
// No ctx parameter: this signature is taken verbatim from the design
// (pg2-24f89/pg2-qye99 D8.1), which deliberately omits one — unlike
// SetMeta/GetMeta/Meta/DeleteMeta, MarkAsLabel is a rare CLI-invocation-time
// call, not a per-turn hot path.
func (s *Store) MarkAsLabel(externalID, key string) error {
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE session_metadata SET is_label = 1 WHERE external_id = ? AND key = ?`,
		externalID, key)
	if err != nil {
		return fmt.Errorf("mark as label %q/%q: %w", externalID, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark as label %q/%q: %w", externalID, key, err)
	}
	if n == 0 {
		return fmt.Errorf("mark as label %q/%q: key not found", externalID, key)
	}
	return nil
}

// Labels returns the subset of externalID's metadata currently marked as
// labels via MarkAsLabel (non-nil empty map when none) (pg2-24f89/pg2-qye99
// D8.1). Like MarkAsLabel, it takes no ctx parameter per the design's stated
// signature.
func (s *Store) Labels(externalID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT key, value FROM session_metadata WHERE external_id = ? AND is_label = 1`,
		externalID)
	if err != nil {
		return nil, fmt.Errorf("labels %q: %w", externalID, err)
	}
	defer func() { _ = rows.Close() }()
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
