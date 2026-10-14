package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Tx is one open store transaction, handed to the callback of InTx or
// PutGroupIfThen. It carries the typed accessors a content write and the change
// row it causes need, so both commit together or neither does.
//
// The store holds ONE pooled connection, and a Tx occupies it. Code running
// inside a transaction MUST use only the Tx's own methods: calling any *Store
// method, or anything that calls one, from inside the callback blocks forever
// (the connection is taken). A Tx MUST NOT be used after its callback returns.
type Tx struct {
	tx *sql.Tx
}

// InTx runs fn in one transaction. It commits when fn returns nil and rolls back
// when fn returns an error (which InTx returns as it is, so errors.Is and
// errors.As see through it) or panics (the panic continues after the rollback).
// Two concurrent InTx calls serialise on the pooled connection.
func (s *Store) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	return s.runTx(ctx, func(tx *sql.Tx) error { return fn(&Tx{tx: tx}) })
}

// runTx begins a transaction, runs fn and commits; an error or panic from fn
// rolls the transaction back.
func (s *Store) runTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	done := false
	defer func() {
		// Covers an error return and a panic alike; after a commit it is a no-op.
		if !done {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	done = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	return nil
}

// PutGroupIfThen is PutGroupIf, and ONLY when it stored the row it calls then in
// the same transaction. An error (or panic) from then rolls the whole
// transaction back, so the group is not stored, and the error is returned with
// stored false.
func (s *Store) PutGroupIfThen(ctx context.Context, g GroupRow, expectGeneration int64, then func(tx *Tx) error) (stored bool, err error) {
	err = s.runTx(ctx, func(tx *sql.Tx) error {
		ok, err := putGroupIf(ctx, tx, g, expectGeneration)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if then != nil {
			if err := then(&Tx{tx: tx}); err != nil {
				return err
			}
		}
		stored = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

// SetKeepAlive updates the keep_alive column of (kind, id) and nothing else, so
// a keep-alive flip never rewrites version. An absent row is left absent.
func (s *Store) SetKeepAlive(ctx context.Context, kind, id string, keep bool) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE entity SET keep_alive = ? WHERE kind = ? AND id = ?`, boolInt(keep), kind, id); err != nil {
		return fmt.Errorf("store: set keep-alive %s/%s: %w", kind, id, err)
	}
	return nil
}

// ---- the Store accessors' transaction forms -----------------------------

// GetEntity is Store.GetEntity inside the transaction.
func (t *Tx) GetEntity(ctx context.Context, kind, id string) (EntityRow, bool, error) {
	return getEntity(ctx, t.tx, kind, id)
}

// PutEntity is Store.PutEntity inside the transaction.
func (t *Tx) PutEntity(ctx context.Context, e EntityRow) error { return putEntity(ctx, t.tx, e) }

// GetGroup is Store.GetGroup inside the transaction.
func (t *Tx) GetGroup(ctx context.Context, kind, id, group string) (GroupRow, bool, error) {
	return getGroup(ctx, t.tx, kind, id, group)
}

// PutGroup is Store.PutGroup inside the transaction.
func (t *Tx) PutGroup(ctx context.Context, g GroupRow) error { return putGroup(ctx, t.tx, g) }

// PutGroupIf is Store.PutGroupIf inside the transaction: the same generation
// compare-and-swap, committing or rolling back with the rest of the transaction.
func (t *Tx) PutGroupIf(ctx context.Context, g GroupRow, expectGeneration int64) (stored bool, err error) {
	return putGroupIf(ctx, t.tx, g, expectGeneration)
}

// GetQuery is Store.GetQuery inside the transaction.
func (t *Tx) GetQuery(ctx context.Context, key string) (QueryRow, bool, error) {
	return getQuery(ctx, t.tx, key)
}

// PutQuery is Store.PutQuery inside the transaction.
func (t *Tx) PutQuery(ctx context.Context, q QueryRow) error { return putQuery(ctx, t.tx, q) }

// ListQueries is Store.ListQueries inside the transaction.
func (t *Tx) ListQueries(ctx context.Context, configuredOnly bool) ([]QueryRow, error) {
	return listQueries(ctx, t.tx, configuredOnly)
}

// EnsureEntity is Store.EnsureEntity inside the transaction: it inserts the
// entity row with its defaults (version 0, no last access, keep_alive 0, no
// tombstone) when it is absent and changes nothing when it exists.
func (t *Tx) EnsureEntity(ctx context.Context, kind, id string) error {
	return ensureEntity(ctx, t.tx, kind, id)
}

// BumpEntityVersion increments entity.version by one in place, touching no other
// column, and returns the new value. A missing entity row is an error (use
// EnsureEntity first for an entity the store has not seen yet).
func (t *Tx) BumpEntityVersion(ctx context.Context, kind, id string) (version int64, err error) {
	err = t.tx.QueryRowContext(ctx,
		`UPDATE entity SET version = version + 1 WHERE kind = ? AND id = ? RETURNING version`, kind, id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: bump entity version %s/%s: no such entity", kind, id)
	}
	if err != nil {
		return 0, fmt.Errorf("store: bump entity version %s/%s: %w", kind, id, err)
	}
	return version, nil
}
