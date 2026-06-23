package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Tx is a store transaction with helpers to mutate state AND enqueue the event
// that resulted, so both commit or roll back together.
type Tx struct {
	tx  *sql.Tx
	ctx context.Context
}

// InTx runs fn in a transaction. If fn returns an error the tx rolls back (and
// any enqueued outbox rows vanish). On nil it commits.
func (db *DB) InTx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	t := &Tx{tx: sqlTx, ctx: ctx}
	if err := fn(t); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// EnqueueEvent writes a pending outbox row inside the transaction.
func (t *Tx) EnqueueEvent(eventType string, payload json.RawMessage) error {
	_, err := t.tx.ExecContext(t.ctx,
		"INSERT INTO outbox (type, payload, status, created_at) VALUES (?,?, 'pending', ?)",
		eventType, string(payload), nowRFC3339())
	if err != nil {
		return fmt.Errorf("store: enqueue %s: %w", eventType, err)
	}
	return nil
}

// Exec runs a statement inside the transaction (used by *Tx mutator variants).
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

// DispatchFunc handles one event. Returning an error is logged by RunOutbox but
// does NOT prevent the row from completing (fire-once, at-least-once).
type DispatchFunc func(ctx context.Context, e Event) error

// RunOutbox pulls each pending row, dispatches it, then marks it complete
// regardless of the dispatch outcome. Returns the first I/O error (not handler
// errors).
func (db *DB) RunOutbox(ctx context.Context, dispatch DispatchFunc) error {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT id, type, payload FROM outbox WHERE status='pending' ORDER BY id")
	if err != nil {
		return fmt.Errorf("store: select pending outbox: %w", err)
	}
	type pend struct {
		id int64
		e  Event
	}
	var pending []pend
	for rows.Next() {
		var p pend
		var payload string
		if err := rows.Scan(&p.id, &p.e.Type, &payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan outbox: %w", err)
		}
		p.e.Payload = json.RawMessage(payload)
		pending = append(pending, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pending {
		_ = dispatch(ctx, p.e)
		if _, err := db.sql.ExecContext(ctx,
			"UPDATE outbox SET status='complete', completed_at=? WHERE id=?",
			nowRFC3339(), p.id); err != nil {
			return fmt.Errorf("store: complete outbox %d: %w", p.id, err)
		}
	}
	return nil
}
