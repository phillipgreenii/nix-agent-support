package store

import (
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
}

// RecordIngestFailure records f. A repeat of the same (Instance, EntityID,
// Seq) updates LastError only and keeps FirstFailedAt. An empty
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
	if _, err := s.sql.Exec(
		`INSERT INTO ingest_failed
		   (instance, entity_id, seq, store_identity, kinds, first_failed_at, last_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (instance, entity_id, seq) DO UPDATE SET last_error = excluded.last_error`,
		f.Instance, f.EntityID, f.Seq, f.StoreIdentity, string(kindsJSON), first, f.LastError,
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
	q := `SELECT instance, entity_id, seq, store_identity, kinds, first_failed_at, last_error FROM ingest_failed`
	var args []any
	if instance != "" {
		q += ` WHERE instance = ?`
		args = append(args, instance)
	}
	q += ` ORDER BY seq, instance, entity_id`
	rows, err := s.sql.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list ingest failures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []IngestFailure
	for rows.Next() {
		var f IngestFailure
		var kinds string
		if err := rows.Scan(&f.Instance, &f.EntityID, &f.Seq, &f.StoreIdentity, &kinds, &f.FirstFailedAt, &f.LastError); err != nil {
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
	if _, err := s.sql.Exec(
		`DELETE FROM ingest_failed WHERE instance = ? AND entity_id = ? AND seq = ?`,
		instance, entityID, seq,
	); err != nil {
		return fmt.Errorf("store: clear ingest failure (%s,%s,%d): %w", instance, entityID, seq, err)
	}
	return nil
}

// OldestIngestFailureAt returns the earliest first_failed_at over every
// entry, and ok=false when there is none (or the table is absent).
func (s *Store) OldestIngestFailureAt() (at time.Time, ok bool, err error) {
	exists, err := s.ingestFailedExists()
	if err != nil || !exists {
		return time.Time{}, false, err
	}
	rows, err := s.sql.Query(`SELECT first_failed_at FROM ingest_failed`)
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
	if err := s.sql.QueryRow(q, args...).Scan(&n); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("store: count ingest failures: %w", err)
	}
	return n, nil
}
