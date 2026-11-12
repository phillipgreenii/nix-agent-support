package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// IngestFailure is one row of the ingest_failed table: a change-feed row of
// a backend instance that could not be hydrated. It is keyed by
// (Instance, EntityID, Seq); StoreIdentity is the identity of the backend's
// own store the Seq belongs to; FirstFailedAt is an RFC3339 time.
type IngestFailure struct {
	Instance      string
	EntityID      string
	Seq           int64
	StoreIdentity string
	Kinds         []string
	FirstFailedAt string
	LastError     string
	// FailureClass is the typed class of the last failure (a connector wire
	// error code such as not_found or unavailable); "" when it carried none.
	FailureClass string
	// FlaggedAt is an RFC3339 time: set once the failure's class is permanent,
	// after which the entry is no longer retried; "" while it is retryable.
	FlaggedAt string
}

// Flagged reports whether the entry is flagged (permanent, not retried).
func (f IngestFailure) Flagged() bool { return f.FlaggedAt != "" }

// RecordIngestFailure records f. A repeat of the same (Instance, EntityID,
// Seq) updates LastError and FailureClass, keeps FirstFailedAt, and sets
// FlaggedAt only when the entry is not flagged yet. An empty
// FirstFailedAt on a first record is stamped with the current UTC time.
func (s *Store) RecordIngestFailure(f IngestFailure) error {
	kinds := f.Kinds
	if kinds == nil {
		kinds = []string{}
	}
	kindsJSON, err := json.Marshal(kinds)
	if err != nil {
		return fmt.Errorf("store: record ingest failure: encode kinds: %w", err)
	}
	first := f.FirstFailedAt
	if first == "" {
		first = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := s.sql.ExecContext(
		context.Background(),
		`INSERT INTO ingest_failed
		   (instance, entity_id, seq, store_identity, kinds, first_failed_at, last_error, failure_class, flagged_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (instance, entity_id, seq) DO UPDATE SET
		   last_error = excluded.last_error,
		   failure_class = excluded.failure_class,
		   flagged_at = CASE WHEN ingest_failed.flagged_at = '' THEN excluded.flagged_at ELSE ingest_failed.flagged_at END`,
		f.Instance, f.EntityID, f.Seq, f.StoreIdentity, string(kindsJSON), first, f.LastError, f.FailureClass, f.FlaggedAt,
	); err != nil {
		return fmt.Errorf("store: record ingest failure (%s,%s,%d): %w", f.Instance, f.EntityID, f.Seq, err)
	}
	return nil
}

// ingestFailedExists reports whether the ingest_failed table exists. A store
// opened read-only or raw that predates the table reads as empty.
func (s *Store) ingestFailedExists() (bool, error) {
	cols, err := s.tableColumns("ingest_failed")
	if err != nil {
		return false, err
	}
	return len(cols) > 0, nil
}

// ListIngestFailures returns the entries of instance ordered by Seq ("" =
// every instance; ties break by instance then entity id). A store without
// the table yields none.
func (s *Store) ListIngestFailures(instance string) ([]IngestFailure, error) {
	ok, err := s.ingestFailedExists()
	if err != nil || !ok {
		return nil, err
	}
	cols, err := s.tableColumns("ingest_failed")
	if err != nil {
		return nil, err
	}
	// A store opened read-only that predates the added columns reads them as "".
	class, flagged := `''`, `''`
	if cols["failure_class"] {
		class = "failure_class"
	}
	if cols["flagged_at"] {
		flagged = "flagged_at"
	}
	q := `SELECT instance, entity_id, seq, store_identity, kinds, first_failed_at, last_error, ` + class + `, ` + flagged + ` FROM ingest_failed`
	var args []any
	if instance != "" {
		q += ` WHERE instance = ?`
		args = append(args, instance)
	}
	q += ` ORDER BY seq, instance, entity_id`
	rows, err := s.sql.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list ingest failures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []IngestFailure
	for rows.Next() {
		var f IngestFailure
		var kinds string
		if err := rows.Scan(&f.Instance, &f.EntityID, &f.Seq, &f.StoreIdentity, &kinds, &f.FirstFailedAt, &f.LastError, &f.FailureClass, &f.FlaggedAt); err != nil {
			return nil, fmt.Errorf("store: scan ingest failure: %w", err)
		}
		if err := json.Unmarshal([]byte(kinds), &f.Kinds); err != nil {
			return nil, fmt.Errorf("store: decode ingest failure kinds (%s,%s,%d): %w", f.Instance, f.EntityID, f.Seq, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list ingest failures: %w", err)
	}
	return out, nil
}

// ClearIngestFailure removes the entry for (instance, entityID, seq); an
// absent entry is not an error.
func (s *Store) ClearIngestFailure(instance, entityID string, seq int64) error {
	if _, err := s.sql.ExecContext(
		context.Background(),
		`DELETE FROM ingest_failed WHERE instance = ? AND entity_id = ? AND seq = ?`,
		instance, entityID, seq,
	); err != nil {
		return fmt.Errorf("store: clear ingest failure (%s,%s,%d): %w", instance, entityID, seq, err)
	}
	return nil
}

// ClearFlaggedIngestFailures removes the flagged entries of (instance,
// entityID): a later successful commit for the entity resolves them. Entries
// that are not flagged are left to their own retry, which still owes their
// recorded kinds. It reports how many entries it removed.
func (s *Store) ClearFlaggedIngestFailures(instance, entityID string) (int, error) {
	ok, err := s.ingestFailedExists()
	if err != nil || !ok {
		return 0, err
	}
	res, err := s.sql.ExecContext(
		context.Background(),
		`DELETE FROM ingest_failed WHERE instance = ? AND entity_id = ? AND flagged_at <> ''`,
		instance, entityID,
	)
	if err != nil {
		return 0, fmt.Errorf("store: clear flagged ingest failures (%s,%s): %w", instance, entityID, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// OldestIngestFailureAt returns the earliest first_failed_at over every
// entry, and ok=false when there is none (or the table is absent).
func (s *Store) OldestIngestFailureAt() (at time.Time, ok bool, err error) {
	exists, err := s.ingestFailedExists()
	if err != nil || !exists {
		return time.Time{}, false, err
	}
	rows, err := s.sql.QueryContext(context.Background(), `SELECT first_failed_at FROM ingest_failed`)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: oldest ingest failure: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return time.Time{}, false, fmt.Errorf("store: oldest ingest failure: scan: %w", err)
		}
		t, perr := time.Parse(time.RFC3339Nano, raw)
		if perr != nil {
			return time.Time{}, false, fmt.Errorf("store: oldest ingest failure: parse %q: %w", raw, perr)
		}
		if !ok || t.Before(at) {
			at, ok = t, true
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("store: oldest ingest failure: %w", err)
	}
	return at, ok, nil
}

// CountIngestFailures returns the number of entries of instance ("" = every
// instance). A store without the table counts zero.
func (s *Store) CountIngestFailures(instance string) (int, error) {
	exists, err := s.ingestFailedExists()
	if err != nil || !exists {
		return 0, err
	}
	q := `SELECT COUNT(*) FROM ingest_failed`
	var args []any
	if instance != "" {
		q += ` WHERE instance = ?`
		args = append(args, instance)
	}
	var n int
	if err := s.sql.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("store: count ingest failures: %w", err)
	}
	return n, nil
}
