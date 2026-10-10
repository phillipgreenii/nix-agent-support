package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	return s.writeEntity(e, expectedVersion, nil, kinds, origin, at)
}

// entityState is the extra pair WriteEntityStateWithLog sets in the same
// statement as the snapshot.
type entityState struct {
	hydratedAt string
	active     bool
	// listFP, when non-nil, is written to list_fp in the same statement;
	// nil leaves list_fp untouched.
	listFP *string
}

// WriteEntityStateWithLog is WriteEntityWithLog that additionally sets
// hydrated_at (hydratedAt, "" meaning NULL) and active in the same
// UPDATE/INSERT, so they commit or roll back together with the snapshot, the
// version bump and the change_log row. Same compare-and-set, same
// ErrVersionConflict and ConflictCount behavior, new schema only. When kinds
// is empty it still updates the row and bumps the version (a re-hydration
// must refresh hydrated_at) but appends NO change_log row.
func (s *Store) WriteEntityStateWithLog(e Entity, expectedVersion int64, hydratedAt string, active bool, kinds []string, origin, at string) (int64, error) {
	return s.writeEntity(e, expectedVersion, &entityState{hydratedAt: hydratedAt, active: active}, kinds, origin, at)
}

// WriteEntityStateWithLogFP is WriteEntityStateWithLog that additionally sets
// list_fp (the list fingerprint the caller observed at this hydration) in the
// same UPDATE/INSERT and transaction when listFP is non-nil, so it commits or
// rolls back together with the snapshot, the version bump, hydrated_at,
// active and the change_log row. A nil listFP leaves list_fp untouched. The
// store never computes the fingerprint. Same compare-and-set, same
// ErrVersionConflict and ConflictCount behavior, new schema only.
func (s *Store) WriteEntityStateWithLogFP(e Entity, expectedVersion int64, hydratedAt string, active bool, listFP *string, kinds []string, origin, at string) (int64, error) {
	return s.writeEntity(e, expectedVersion, &entityState{hydratedAt: hydratedAt, active: active, listFP: listFP}, kinds, origin, at)
}

// SetBetweenBumpAndAppendHook installs fn as a fault-injection seam run
// inside WriteEntityWithLog's, WriteEntityStateWithLog's and
// WriteEntityStateWithLogFP's transaction after
// the entity write and before the change_log append; a non-nil return aborts
// and rolls back the write. Passing nil clears it. Test use only.
func (s *Store) SetBetweenBumpAndAppendHook(fn func() error) { s.betweenBumpAndAppend = fn }

// writeEntity is the shared compare-and-set write. A nil state leaves
// hydrated_at and active untouched (WriteEntityWithLog); a non-nil state sets
// them and skips the change_log append when kinds is empty.
func (s *Store) writeEntity(e Entity, expectedVersion int64, state *entityState, kinds []string, origin, at string) (int64, error) {
	if err := s.RequireNewSchema(); err != nil {
		return 0, err
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: write entity (%s,%s,%s): begin: %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	newVersion := expectedVersion + 1
	updateSQL := `UPDATE entity SET facts = ?, as_of = ?, stale = ?, content_hash = ?, head_sha = ?, version = ?`
	updateArgs := []any{e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA), newVersion}
	insertCols := `repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha, version`
	insertArgs := []any{e.Repo, e.EntityType, e.EntityID, e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA), newVersion}
	// first_seen_at is stamped by the INSERT only (the write's own time) and
	// is deliberately absent from updateSQL, so an update never rewrites it.
	insertCols += `, first_seen_at`
	insertArgs = append(insertArgs, nullableString(at))
	if state != nil {
		activeInt := 0
		if state.active {
			activeInt = 1
		}
		updateSQL += `, hydrated_at = ?, active = ?`
		updateArgs = append(updateArgs, nullableString(state.hydratedAt), activeInt)
		insertCols += `, hydrated_at, active`
		insertArgs = append(insertArgs, nullableString(state.hydratedAt), activeInt)
		if state.listFP != nil {
			updateSQL += `, list_fp = ?`
			updateArgs = append(updateArgs, *state.listFP)
			insertCols += `, list_fp`
			insertArgs = append(insertArgs, *state.listFP)
		}
	}
	updateSQL += ` WHERE repo = ? AND entity_type = ? AND entity_id = ? AND version = ?`
	updateArgs = append(updateArgs, e.Repo, e.EntityType, e.EntityID, expectedVersion)

	res, err := tx.Exec(updateSQL, updateArgs...)
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
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(insertArgs)), ", ")
		ins, err := tx.Exec(
			`INSERT INTO entity (`+insertCols+`) VALUES (`+placeholders+`)
			 ON CONFLICT (repo, entity_type, entity_id) DO NOTHING`,
			insertArgs...,
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
	if state == nil || len(kinds) > 0 {
		if _, err := s.AppendChangeLogTx(tx, ChangeRecord{
			Repo: e.Repo, EntityType: e.EntityType, EntityID: e.EntityID,
			Version: newVersion, Kinds: kinds, Origin: origin, At: at,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: write entity (%s,%s,%s): commit: %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	return newVersion, nil
}

// AppendEntityChange appends one change_log record for an entity other than
// the one being written, at that entity's CURRENT version, in one
// transaction. It does NOT bump the version (like SetAnnotation's record) and
// returns ErrNoEntity (matchable with errors.Is) when the entity row does not
// exist. It requires the new schema.
func (s *Store) AppendEntityChange(repo, entityType, entityID string, kinds []string, origin, at string) (int64, error) {
	if err := s.RequireNewSchema(); err != nil {
		return 0, err
	}
	wrap := func(err error) error {
		return fmt.Errorf("store: append entity change (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return 0, wrap(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var version int64
	err = tx.QueryRow(
		`SELECT version FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, wrap(ErrNoEntity)
	}
	if err != nil {
		return 0, wrap(err)
	}
	seq, err := s.AppendChangeLogTx(tx, ChangeRecord{
		Repo: repo, EntityType: entityType, EntityID: entityID,
		Version: version, Kinds: kinds, Origin: origin, At: at,
	})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, wrap(err)
	}
	return seq, nil
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

// LatestChangeAt returns, per entity of entityType, the newest change_log.at
// (max(at), RFC3339 as written) keyed by entity id. An entity with no
// change_log row is absent from the map: change_log is pruned and a reset or
// backfilled entity may hold none. Nothing is stored for this: it is derived
// on every read, so no column can drift from the log. It requires the new
// schema.
func (s *Store) LatestChangeAt(entityType string) (map[string]string, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(
		`SELECT entity_id, MAX(at) FROM change_log WHERE entity_type = ? GROUP BY entity_id`,
		entityType,
	)
	if err != nil {
		return nil, fmt.Errorf("store: latest change_log time: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("store: scan latest change_log time: %w", err)
		}
		out[id] = at
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: latest change_log time: %w", err)
	}
	return out, nil
}
