package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The change log and entity-version primitives (schema version 2 only).
//
// change_log is the append-only record of every change to an entity;
// entity.version is the optimistic-concurrency version of the entity row.
// The two move together: WriteEntityWithLog conditions the entity write on
// the version the caller read, bumps it, and appends the log row in ONE
// transaction, so a log record's version always names a snapshot that
// exists and an older snapshot can never overwrite a newer one. The retry
// policy on a lost race (re-read, re-classify, write again) belongs to the
// caller; this layer only reports the conflict and counts it.

// ErrVersionConflict is returned (matchable with errors.Is) by
// WriteEntityWithLog when the entity's stored version is not the one the
// caller read: a newer snapshot exists, or a concurrent first writer won.
var ErrVersionConflict = errors.New("entity version conflict")

// ChangeRecord is one row of change_log. Kinds is the JSON-array column
// decoded; Version names the entity snapshot the change produced.
type ChangeRecord struct {
	Seq        int64
	Repo       string
	EntityType string
	EntityID   string
	Version    int64
	Kinds      []string
	Origin     string
	At         string // RFC3339 UTC
}

// ConflictCount returns how many entity writes on this Store handle have
// lost an optimistic-version race since it was opened (the observability
// metric's source).
func (s *Store) ConflictCount() int64 { return s.conflicts.Load() }

// WriteEntityWithLog writes e (facts, as_of, stale, content_hash, head_sha)
// conditional on the stored version equalling expectedVersion, and in the
// same transaction sets version = expectedVersion+1 and appends a change_log
// row (kinds, origin, at) for that new version. It returns the new version.
//
// When no row exists and expectedVersion is 0 the entity is inserted with
// version 1; a row that the cutover left at version 0 is updated in place.
// Any mismatch (stale version, nonzero expectedVersion on an absent row, or
// losing a race with another first writer) returns ErrVersionConflict, counts
// it in ConflictCount, and leaves the store untouched. It requires the new
// schema.
func (s *Store) WriteEntityWithLog(e Entity, expectedVersion int64, kinds []string, origin, at string) (int64, error) {
	if err := s.RequireNewSchema(); err != nil {
		return 0, err
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: write entity (%s,%s,%s): begin: %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	newVersion := expectedVersion + 1
	res, err := tx.Exec(
		`UPDATE entity SET facts = ?, as_of = ?, stale = ?, content_hash = ?, head_sha = ?, version = ?
		 WHERE repo = ? AND entity_type = ? AND entity_id = ? AND version = ?`,
		e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA), newVersion,
		e.Repo, e.EntityType, e.EntityID, expectedVersion,
	)
	if err != nil {
		return 0, fmt.Errorf("store: write entity (%s,%s,%s): %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	matched, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: write entity (%s,%s,%s): %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	if matched == 0 {
		// Nothing matched: either the row is absent or its version differs.
		// Only a first write (expected 0) may create the row; the insert
		// does nothing if a concurrent first writer got there first.
		if expectedVersion != 0 {
			return 0, s.conflict(e)
		}
		ins, err := tx.Exec(
			`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha, version)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (repo, entity_type, entity_id) DO NOTHING`,
			e.Repo, e.EntityType, e.EntityID, e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA), newVersion,
		)
		if err != nil {
			return 0, fmt.Errorf("store: write entity (%s,%s,%s): insert: %w", e.Repo, e.EntityType, e.EntityID, err)
		}
		if n, err := ins.RowsAffected(); err != nil {
			return 0, fmt.Errorf("store: write entity (%s,%s,%s): insert: %w", e.Repo, e.EntityType, e.EntityID, err)
		} else if n == 0 {
			return 0, s.conflict(e)
		}
	}

	if s.betweenBumpAndAppend != nil {
		if err := s.betweenBumpAndAppend(); err != nil {
			return 0, fmt.Errorf("store: write entity (%s,%s,%s): %w", e.Repo, e.EntityType, e.EntityID, err)
		}
	}
	if _, err := s.AppendChangeLogTx(tx, ChangeRecord{
		Repo: e.Repo, EntityType: e.EntityType, EntityID: e.EntityID,
		Version: newVersion, Kinds: kinds, Origin: origin, At: at,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: write entity (%s,%s,%s): commit: %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	return newVersion, nil
}

// conflict counts a lost race and builds the typed error.
func (s *Store) conflict(e Entity) error {
	s.conflicts.Add(1)
	return fmt.Errorf("store: write entity (%s,%s,%s): %w", e.Repo, e.EntityType, e.EntityID, ErrVersionConflict)
}

// AppendChangeLogTx appends r to change_log inside the caller's transaction
// and returns its seq (r.Seq is ignored). It is the append-only primitive:
// nothing updates or deletes rows here. A nil Kinds is stored as [].
func (s *Store) AppendChangeLogTx(tx *sql.Tx, r ChangeRecord) (int64, error) {
	kinds := r.Kinds
	if kinds == nil {
		kinds = []string{}
	}
	kindsJSON, err := json.Marshal(kinds)
	if err != nil {
		return 0, fmt.Errorf("store: append change_log: encode kinds: %w", err)
	}
	res, err := tx.Exec(
		`INSERT INTO change_log (repo, entity_type, entity_id, version, kinds, origin, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Repo, r.EntityType, r.EntityID, r.Version, string(kindsJSON), r.Origin, r.At,
	)
	if err != nil {
		return 0, fmt.Errorf("store: append change_log (%s,%s,%s): %w", r.Repo, r.EntityType, r.EntityID, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append change_log (%s,%s,%s): %w", r.Repo, r.EntityType, r.EntityID, err)
	}
	return seq, nil
}

// ListChangesAfter returns change_log records of entityType with seq >
// afterSeq, ascending by seq, at most limit of them (limit <= 0 means no
// limit).
func (s *Store) ListChangesAfter(entityType string, afterSeq int64, limit int) ([]ChangeRecord, error) {
	return s.queryChanges(
		`SELECT seq, repo, entity_type, entity_id, version, kinds, origin, at
		 FROM change_log WHERE entity_type = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		entityType, afterSeq, limitArg(limit),
	)
}

// ListEntityHistory returns one entity's change_log records, newest first,
// at most limit of them (limit <= 0 means no limit).
func (s *Store) ListEntityHistory(repo, entityType, entityID string, limit int) ([]ChangeRecord, error) {
	return s.queryChanges(
		`SELECT seq, repo, entity_type, entity_id, version, kinds, origin, at
		 FROM change_log WHERE repo = ? AND entity_type = ? AND entity_id = ? ORDER BY seq DESC LIMIT ?`,
		repo, entityType, entityID, limitArg(limit),
	)
}

// limitArg maps a non-positive limit to SQLite's "no limit" (-1).
func limitArg(limit int) int {
	if limit <= 0 {
		return -1
	}
	return limit
}

func (s *Store) queryChanges(query string, args ...any) ([]ChangeRecord, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list change_log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ChangeRecord
	for rows.Next() {
		var r ChangeRecord
		var kinds string
		if err := rows.Scan(&r.Seq, &r.Repo, &r.EntityType, &r.EntityID, &r.Version, &kinds, &r.Origin, &r.At); err != nil {
			return nil, fmt.Errorf("store: scan change_log row: %w", err)
		}
		if err := json.Unmarshal([]byte(kinds), &r.Kinds); err != nil {
			return nil, fmt.Errorf("store: decode change_log kinds %q: %w", kinds, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list change_log: %w", err)
	}
	return out, nil
}
