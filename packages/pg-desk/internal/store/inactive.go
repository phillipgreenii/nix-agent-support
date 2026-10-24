package store

import "fmt"

// MarkEntityInactive sets the entity's active flag to 0 and, in the same
// transaction, adds one to the meta counter counterKey. It reports whether the
// row changed: a row that is absent, or already inactive, changes nothing and
// counts nothing, so marking twice counts once. The version, hydrated_at and
// snapshot are left alone (the entity stays what it was, only no longer
// listed as active), so a later successful hydration, which writes active = 1
// through its compare-and-set, brings the entity back.
//
// It is a store write, not a tracker write, so a run in either lane mode may
// call it. New schema only (the active column does not exist before it).
func (s *Store) MarkEntityInactive(repo, entityType, entityID, counterKey string) (bool, error) {
	if err := s.RequireNewSchema(); err != nil {
		return false, err
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return false, fmt.Errorf("store: mark inactive (%s,%s,%s): begin: %w", repo, entityType, entityID, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	res, err := tx.Exec(
		`UPDATE entity SET active = 0
		  WHERE repo = ? AND entity_type = ? AND entity_id = ? AND active = 1`,
		repo, entityType, entityID,
	)
	if err != nil {
		return false, fmt.Errorf("store: mark inactive (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: mark inactive (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	if n == 0 {
		return false, nil
	}
	if _, err := tx.Exec(
		`INSERT INTO meta (key, value) VALUES (?, '1')
		 ON CONFLICT (key) DO UPDATE SET value = CAST(meta.value AS INTEGER) + excluded.value`,
		counterKey,
	); err != nil {
		return false, fmt.Errorf("store: mark inactive (%s,%s,%s): count: %w", repo, entityType, entityID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: mark inactive (%s,%s,%s): commit: %w", repo, entityType, entityID, err)
	}
	return true, nil
}
