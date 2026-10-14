package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds the two compare-and-swap primitives behind the daemon's
// write-invalidation rule: a refresh that STARTED before a write must not
// overwrite content stored after it. Each group row carries a generation
// number; a reader remembers the generation it saw (PutGroupIf's expect), a
// writer or an invalidation bumps it (BumpGroup), and a store whose expected
// generation no longer matches writes nothing.

// summaryGroupName is the one group a bump never creates: a summary row exists
// only because a summary was fetched, and an "empty" one would read as a
// fetched-but-empty PR. It mirrors the summary package's GroupName, which this
// generic package cannot import.
const summaryGroupName = "summary"

// PutGroupIf stores g only when the stored row's generation equals
// expectGeneration, or when no row exists and expectGeneration is 0. It stores
// g verbatim (the caller chooses g.Generation, normally expectGeneration+1) and
// reports whether it did; false means the row moved on and nothing was
// written. The check and the write are one transaction.
func (s *Store) PutGroupIf(ctx context.Context, g GroupRow, expectGeneration int64) (stored bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: put group if %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
	}
	defer func() { _ = tx.Rollback() }()

	stored, err = putGroupIf(ctx, tx, g, expectGeneration)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: put group if %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
	}
	return stored, nil
}

// putGroupIf is PutGroupIf's compare-and-swap, run on a transaction the caller
// owns (PutGroupIf's own, or a Tx's).
func putGroupIf(ctx context.Context, tx dbtx, g GroupRow, expectGeneration int64) (stored bool, err error) {
	res, err := tx.ExecContext(ctx, `
UPDATE entity_group SET
  content = ?, fingerprint = ?, fetched_at = ?, fresh_until = ?, max_age_at = ?,
  last_error = ?, generation = ?, fetched_for_head = ?, fetched_for_base = ?
WHERE kind = ? AND id = ? AND group_name = ? AND generation = ?`,
		g.Content, g.Fingerprint, msOrNull(g.FetchedAt), msOrNull(g.FreshUntil), msOrNull(g.MaxAgeAt),
		g.LastError, g.Generation, g.FetchedForHead, g.FetchedForBase,
		g.Kind, g.ID, g.Group, expectGeneration)
	if err != nil {
		return false, fmt.Errorf("store: put group if %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: put group if %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
	}
	stored = n == 1
	if !stored && expectGeneration == 0 {
		// No row at generation 0: insert one, unless a row exists at another
		// generation, in which case the conflict keeps it untouched.
		res, err = tx.ExecContext(ctx, `
INSERT INTO entity_group (kind, id, group_name, content, fingerprint, fetched_at, fresh_until,
                          max_age_at, last_error, generation, fetched_for_head, fetched_for_base)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (kind, id, group_name) DO NOTHING`,
			g.Kind, g.ID, g.Group, g.Content, g.Fingerprint, msOrNull(g.FetchedAt), msOrNull(g.FreshUntil),
			msOrNull(g.MaxAgeAt), g.LastError, g.Generation, g.FetchedForHead, g.FetchedForBase)
		if err != nil {
			return false, fmt.Errorf("store: put group if %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("store: put group if %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
		}
		stored = n == 1
	}
	return stored, nil
}

// BumpGroup increments the group's generation and sets its fresh_until
// (freshUntil zero stores NULL), keeping the content, in one transaction, and
// returns the new generation. A missing row is created empty (no content) at
// generation 1, except for the summary group, whose missing row stays missing:
// the call returns generation 0 and writes nothing. A row whose content is empty
// is never served as content.
func (s *Store) BumpGroup(ctx context.Context, kind, id, group string, freshUntil time.Time) (generation int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: bump group %s/%s/%s: %w", kind, id, group, err)
	}
	defer func() { _ = tx.Rollback() }()

	var row interface{ Scan(...any) error }
	if group == summaryGroupName {
		row = tx.QueryRowContext(ctx, `
UPDATE entity_group SET generation = generation + 1, fresh_until = ?
WHERE kind = ? AND id = ? AND group_name = ?
RETURNING generation`, msOrNull(freshUntil), kind, id, group)
	} else {
		row = tx.QueryRowContext(ctx, `
INSERT INTO entity_group (kind, id, group_name, content, fresh_until, generation)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (kind, id, group_name) DO UPDATE SET
  generation = entity_group.generation + 1, fresh_until = excluded.fresh_until
RETURNING generation`, kind, id, group, []byte{}, msOrNull(freshUntil))
	}
	if err := row.Scan(&generation); err != nil {
		if group == summaryGroupName && errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: bump group %s/%s/%s: %w", kind, id, group, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: bump group %s/%s/%s: %w", kind, id, group, err)
	}
	return generation, nil
}
