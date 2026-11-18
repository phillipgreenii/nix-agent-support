package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Consumer cursors (schema version 2 only).
//
// A consumer is a named reader of change_log for one entity type; its row in
// the consumer table holds the cursor (the highest change_log seq it has
// been handed AND confirmed) and seen_at (when it last read). Delivery is
// at-least-once: the store exposes READ and ADVANCE as separate steps so the
// caller flushes its output between them. A crash after the flush but before
// the advance re-delivers the same records (a duplicate); a crash before the
// flush loses nothing either, because the cursor never moved. There is no
// window in which a record is skipped.
//
// The caller brackets read, flush and advance with LockConsumer so two
// concurrent calls for the same (type, consumer) serialize: the second reads
// from the cursor the first advanced.

const (
	// DefaultChangeLogRetention is how long a change_log row is kept, at
	// minimum, when PruneChangeLog is given a zero retention.
	DefaultChangeLogRetention = 14 * 24 * time.Hour
	// DefaultConsumerStaleAfter is how long a consumer may go unseen before
	// PruneChangeLog stops waiting for it, when given a zero stale_after.
	DefaultConsumerStaleAfter = 7 * 24 * time.Hour
)

// Consumer is one row of the consumer table. SeenAt is RFC3339 UTC, empty
// when the column is NULL (registered but never read).
type Consumer struct {
	Name   string
	Type   string
	Cursor int64
	SeenAt string
	// Delivered is the highest change_log seq handed to the consumer, the
	// upper bound of an acknowledgement. A row from before the column, or one
	// never delivered to, reads as the cursor.
	Delivered int64
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RegisterConsumer creates the (name, type) consumer at cursor 0 if absent
// and sets its seen_at to now; an existing consumer keeps its cursor.
func (s *Store) RegisterConsumer(ctx context.Context, name, entityType string, now time.Time) error {
	if err := s.RequireNewSchema(ctx); err != nil {
		return err
	}
	if _, err := s.sql.ExecContext(
		ctx,
		`INSERT INTO consumer (name, type, cursor, seen_at) VALUES (?, ?, 0, ?)
		 ON CONFLICT (name, type) DO UPDATE SET seen_at = excluded.seen_at`,
		name, entityType, formatTime(now),
	); err != nil {
		return fmt.Errorf("store: register consumer (%s,%s): %w", name, entityType, err)
	}
	return nil
}

// RegisterConsumerAtMax is RegisterConsumer for a consumer that must not
// replay history: when (name, type) is absent it is created at the current
// maximum change_log seq of entityType (0 when the type has no row), in one
// statement, so records that already exist are not delivered. An existing
// consumer keeps its cursor and only has its seen_at refreshed, so the call is
// safe on every poll (the start-at-max rule applies to the first registration
// only).
func (s *Store) RegisterConsumerAtMax(ctx context.Context, name, entityType string, now time.Time) error {
	if err := s.RequireNewSchema(ctx); err != nil {
		return err
	}
	if _, err := s.sql.ExecContext(
		ctx,
		`INSERT INTO consumer (name, type, cursor, seen_at)
		 SELECT ?, ?, COALESCE(MAX(seq), 0), ? FROM change_log WHERE entity_type = ?
		 ON CONFLICT (name, type) DO UPDATE SET seen_at = excluded.seen_at`,
		name, entityType, formatTime(now), entityType,
	); err != nil {
		return fmt.Errorf("store: register consumer at max (%s,%s): %w", name, entityType, err)
	}
	return nil
}

// ReadChanges registers the consumer if needed, stamps its seen_at with now,
// and returns up to limit change_log records of entityType after its cursor
// (limit <= 0 means no limit), ascending by seq. It does NOT advance the
// cursor: the caller flushes the records, then calls AdvanceCursor with the
// last seq it delivered. Repeated reads without an advance return the same
// records.
func (s *Store) ReadChanges(ctx context.Context, name, entityType string, limit int, now time.Time) ([]ChangeRecord, error) {
	if err := s.RegisterConsumer(ctx, name, entityType, now); err != nil {
		return nil, err
	}
	return s.PeekChanges(ctx, name, entityType, limit)
}

// PeekChanges is ReadChanges without any side effect: it neither registers
// the consumer, nor touches seen_at, nor moves the cursor (the read behind
// `--cached`). An unregistered consumer peeks from cursor 0.
func (s *Store) PeekChanges(ctx context.Context, name, entityType string, limit int) ([]ChangeRecord, error) {
	if err := s.RequireNewSchema(ctx); err != nil {
		return nil, err
	}
	var cursor int64
	err := s.sql.QueryRowContext(ctx, `SELECT cursor FROM consumer WHERE name = ? AND type = ?`, name, entityType).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read consumer cursor (%s,%s): %w", name, entityType, err)
	}
	return s.ListChangesAfter(ctx, entityType, cursor, limit)
}

// ErrConsumerNotFound is returned (matchable with errors.Is) when a cursor
// operation names a consumer that is not registered.
var ErrConsumerNotFound = errors.New("consumer not registered")

// AdvanceCursor moves the (name, type) consumer's cursor forward to seq, the
// last seq the caller has flushed. It only ever moves forward: a seq at or
// below the current cursor changes nothing (it is not an error, so a retried
// advance is harmless). An unregistered consumer returns ErrConsumerNotFound.
func (s *Store) AdvanceCursor(ctx context.Context, name, entityType string, seq int64) error {
	if err := s.RequireNewSchema(ctx); err != nil {
		return err
	}
	res, err := s.sql.ExecContext(ctx, `UPDATE consumer SET cursor = ?, delivered = MAX(COALESCE(delivered, cursor), ?) WHERE name = ? AND type = ? AND cursor < ?`,
		seq, seq, name, entityType, seq)
	if err != nil {
		return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, err)
	} else if n == 0 {
		var one int
		qerr := s.sql.QueryRowContext(ctx, `SELECT 1 FROM consumer WHERE name = ? AND type = ?`, name, entityType).Scan(&one)
		if errors.Is(qerr, sql.ErrNoRows) {
			return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, ErrConsumerNotFound)
		} else if qerr != nil {
			return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, qerr)
		}
	}
	return nil
}

// ErrAckBeyondDelivered is returned (matchable with errors.Is) when an
// acknowledgement names a seq above the highest seq delivered to the consumer.
var ErrAckBeyondDelivered = errors.New("acknowledgement is past the highest delivered sequence")

// MarkDelivered raises the consumer's delivered value to seq (never lowers it)
// without moving the cursor: the bookkeeping of a --no-ack emit. An
// unregistered consumer returns ErrConsumerNotFound.
func (s *Store) MarkDelivered(ctx context.Context, name, entityType string, seq int64) error {
	if err := s.RequireNewSchema(ctx); err != nil {
		return err
	}
	res, err := s.sql.ExecContext(ctx, `UPDATE consumer SET delivered = MAX(COALESCE(delivered, cursor), ?) WHERE name = ? AND type = ?`,
		seq, name, entityType)
	if err != nil {
		return fmt.Errorf("store: mark delivered (%s,%s): %w", name, entityType, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: mark delivered (%s,%s): %w", name, entityType, err)
	} else if n == 0 {
		return fmt.Errorf("store: mark delivered (%s,%s): %w", name, entityType, ErrConsumerNotFound)
	}
	return nil
}

// AckCursor is the deferred acknowledgement: it moves the cursor forward to
// seq and returns the cursor afterwards (above seq only when it already was).
// A seq at or below the cursor is a no-op, never a backward move. An
// unregistered consumer returns ErrConsumerNotFound (checked first), and a seq
// above the delivered value returns ErrAckBeyondDelivered. The caller holds
// the consumer lock.
func (s *Store) AckCursor(ctx context.Context, name, entityType string, seq int64) (int64, error) {
	if err := s.RequireNewSchema(ctx); err != nil {
		return 0, err
	}
	var cursor, delivered int64
	err := s.sql.QueryRowContext(ctx, `SELECT cursor, COALESCE(delivered, cursor) FROM consumer WHERE name = ? AND type = ?`,
		name, entityType).Scan(&cursor, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: ack consumer (%s,%s): %w", name, entityType, ErrConsumerNotFound)
	} else if err != nil {
		return 0, fmt.Errorf("store: ack consumer (%s,%s): %w", name, entityType, err)
	}
	if seq > delivered {
		return cursor, fmt.Errorf("store: ack consumer (%s,%s): %d > %d: %w", name, entityType, seq, delivered, ErrAckBeyondDelivered)
	}
	if seq <= cursor {
		return cursor, nil
	}
	if err := s.AdvanceCursor(ctx, name, entityType, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// ResetConsumer moves the consumer's cursor back to 0 so the next read
// replays the log from the start (the only backward move). An unregistered
// consumer returns ErrConsumerNotFound.
func (s *Store) ResetConsumer(ctx context.Context, name, entityType string) error {
	if err := s.RequireNewSchema(ctx); err != nil {
		return err
	}
	res, err := s.sql.ExecContext(ctx, `UPDATE consumer SET cursor = 0 WHERE name = ? AND type = ?`, name, entityType)
	if err != nil {
		return fmt.Errorf("store: reset consumer (%s,%s): %w", name, entityType, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: reset consumer (%s,%s): %w", name, entityType, err)
	} else if n == 0 {
		return fmt.Errorf("store: reset consumer (%s,%s): %w", name, entityType, ErrConsumerNotFound)
	}
	return nil
}

// ListConsumers returns every registered consumer ordered by (type, name).
func (s *Store) ListConsumers(ctx context.Context) ([]Consumer, error) {
	if err := s.RequireNewSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.sql.QueryContext(ctx, `SELECT name, type, cursor, seen_at, COALESCE(delivered, cursor) FROM consumer ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("store: list consumers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Consumer
	for rows.Next() {
		var c Consumer
		var seen sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &c.Cursor, &seen, &c.Delivered); err != nil {
			return nil, fmt.Errorf("store: scan consumer: %w", err)
		}
		c.SeenAt = seen.String
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list consumers: %w", err)
	}
	return out, nil
}

// ForgetConsumer deletes the (name, type) consumer. Forgetting an absent
// consumer is not an error.
func (s *Store) ForgetConsumer(ctx context.Context, name, entityType string) error {
	if err := s.RequireNewSchema(ctx); err != nil {
		return err
	}
	if _, err := s.sql.ExecContext(ctx, `DELETE FROM consumer WHERE name = ? AND type = ?`, name, entityType); err != nil {
		return fmt.Errorf("store: forget consumer (%s,%s): %w", name, entityType, err)
	}
	return nil
}

// SetConsumerLockerOptions replaces the Locker LockConsumer uses. Tests MUST
// use it to inject a temp LockDir instead of the runtime-dir default.
func (s *Store) SetConsumerLockerOptions(opts LockerOptions) { s.consumerLocker = NewLocker(opts) }

// LockConsumer takes the cross-process advisory lock for one (type, consumer)
// pair. The caller holds it across read, flush and advance and calls unlock
// exactly once. Different consumers or types do not contend.
func (s *Store) LockConsumer(consumerType, name string) (unlock func(), err error) {
	l := s.consumerLocker
	if l == nil {
		l = NewLocker(LockerOptions{})
	}
	return l.Lock(name, consumerType, "changes")
}

// PruneChangeLog deletes change_log rows that (a) are older than retention
// (by their RFC3339 UTC `at`) and (b) every non-stale consumer of the row's
// entity type has already passed. It returns how many rows were deleted.
//
// The horizon is per entity type: only consumers registered for that type
// hold its rows back. A consumer whose seen_at is NULL, or older than
// staleAfter before now, is stale and excluded. With no non-stale consumer
// for a type, every row older than retention is prunable. A zero (or
// negative) retention or staleAfter means the design default (14 and 7 days).
func (s *Store) PruneChangeLog(ctx context.Context, now time.Time, retention, staleAfter time.Duration) (int64, error) {
	if err := s.RequireNewSchema(ctx); err != nil {
		return 0, err
	}
	if retention <= 0 {
		retention = DefaultChangeLogRetention
	}
	if staleAfter <= 0 {
		staleAfter = DefaultConsumerStaleAfter
	}
	oldEnough := formatTime(now.Add(-retention))
	staleBefore := formatTime(now.Add(-staleAfter))

	// Per type: the slowest non-stale consumer's cursor, or unbounded (the
	// max int64) when there is none. Rows with seq <= that cursor have been
	// delivered to everyone who is still waiting.
	res, err := s.sql.ExecContext(
		ctx,
		`DELETE FROM change_log
		 WHERE at < ?
		   AND seq <= COALESCE(
		     (SELECT MIN(c.cursor) FROM consumer c
		      WHERE c.type = change_log.entity_type
		        AND c.seen_at IS NOT NULL AND c.seen_at >= ?),
		     9223372036854775807)`,
		oldEnough, staleBefore,
	)
	if err != nil {
		return 0, fmt.Errorf("store: prune change_log: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune change_log: %w", err)
	}
	return n, nil
}
