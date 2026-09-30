package store

import (
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
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RegisterConsumer creates the (name, type) consumer at cursor 0 if absent
// and sets its seen_at to now; an existing consumer keeps its cursor.
func (s *Store) RegisterConsumer(name, entityType string, now time.Time) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	if _, err := s.sql.Exec(
		`INSERT INTO consumer (name, type, cursor, seen_at) VALUES (?, ?, 0, ?)
		 ON CONFLICT (name, type) DO UPDATE SET seen_at = excluded.seen_at`,
		name, entityType, formatTime(now),
	); err != nil {
		return fmt.Errorf("store: register consumer (%s,%s): %w", name, entityType, err)
	}
	return nil
}

// ReadChanges registers the consumer if needed, stamps its seen_at with now,
// and returns up to limit change_log records of entityType after its cursor
// (limit <= 0 means no limit), ascending by seq. It does NOT advance the
// cursor: the caller flushes the records, then calls AdvanceCursor with the
// last seq it delivered. Repeated reads without an advance return the same
// records.
func (s *Store) ReadChanges(name, entityType string, limit int, now time.Time) ([]ChangeRecord, error) {
	if err := s.RegisterConsumer(name, entityType, now); err != nil {
		return nil, err
	}
	return s.PeekChanges(name, entityType, limit)
}

// PeekChanges is ReadChanges without any side effect: it neither registers
// the consumer, nor touches seen_at, nor moves the cursor (the read behind
// `--cached`). An unregistered consumer peeks from cursor 0.
func (s *Store) PeekChanges(name, entityType string, limit int) ([]ChangeRecord, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	var cursor int64
	err := s.sql.QueryRow(`SELECT cursor FROM consumer WHERE name = ? AND type = ?`, name, entityType).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read consumer cursor (%s,%s): %w", name, entityType, err)
	}
	return s.ListChangesAfter(entityType, cursor, limit)
}

// ErrConsumerNotFound is returned (matchable with errors.Is) when a cursor
// operation names a consumer that is not registered.
var ErrConsumerNotFound = errors.New("consumer not registered")

// AdvanceCursor moves the (name, type) consumer's cursor forward to seq, the
// last seq the caller has flushed. It only ever moves forward: a seq at or
// below the current cursor changes nothing (it is not an error, so a retried
// advance is harmless). An unregistered consumer returns ErrConsumerNotFound.
func (s *Store) AdvanceCursor(name, entityType string, seq int64) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	res, err := s.sql.Exec(`UPDATE consumer SET cursor = ? WHERE name = ? AND type = ? AND cursor < ?`,
		seq, name, entityType, seq)
	if err != nil {
		return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, err)
	} else if n == 0 {
		var one int
		qerr := s.sql.QueryRow(`SELECT 1 FROM consumer WHERE name = ? AND type = ?`, name, entityType).Scan(&one)
		if errors.Is(qerr, sql.ErrNoRows) {
			return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, ErrConsumerNotFound)
		} else if qerr != nil {
			return fmt.Errorf("store: advance consumer (%s,%s): %w", name, entityType, qerr)
		}
	}
	return nil
}

// ResetConsumer moves the consumer's cursor back to 0 so the next read
// replays the log from the start (the only backward move). An unregistered
// consumer returns ErrConsumerNotFound.
func (s *Store) ResetConsumer(name, entityType string) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	res, err := s.sql.Exec(`UPDATE consumer SET cursor = 0 WHERE name = ? AND type = ?`, name, entityType)
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
func (s *Store) ListConsumers() ([]Consumer, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT name, type, cursor, seen_at FROM consumer ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("store: list consumers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Consumer
	for rows.Next() {
		var c Consumer
		var seen sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &c.Cursor, &seen); err != nil {
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
func (s *Store) ForgetConsumer(name, entityType string) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	if _, err := s.sql.Exec(`DELETE FROM consumer WHERE name = ? AND type = ?`, name, entityType); err != nil {
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
func (s *Store) PruneChangeLog(now time.Time, retention, staleAfter time.Duration) (int64, error) {
	if err := s.RequireNewSchema(); err != nil {
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
	res, err := s.sql.Exec(
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
