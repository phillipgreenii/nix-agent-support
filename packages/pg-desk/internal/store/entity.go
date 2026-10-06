package store

import (
	"database/sql"
	"fmt"
)

// Entity is one row of the entity table: the last gathered facts for a
// single (repo, entity_type, entity_id), as JSON, per the design doc's
// section 7.6. Written by the gather stage (a later packet in this
// docket); this packet exposes the writer/reader only.
//
// Schema-dual: the cutover adds version, hydrated_at and active to the
// entity table, all with defaults. UpsertEntity names only the original
// columns, so it works unchanged on both schema versions: on the new schema
// it leaves the new columns at their defaults (or at whatever the new-schema
// API last set). Reads return the new columns on the new schema and their
// defaults (0, "", active) on the old one.
type Entity struct {
	Repo        string
	EntityType  string
	EntityID    string
	Facts       string // JSON blob of last-gathered facts
	AsOf        string // RFC3339 timestamp
	Stale       bool
	ContentHash string
	HeadSHA     string // set for files/commits only; empty otherwise

	// Version is the optimistic-concurrency version of the row (new schema
	// only; always 0 when read from an old-schema store). Populated by
	// reads; UpsertEntity ignores it. WriteEntityWithLog takes the version
	// the caller read as its expectedVersion and ignores this field.
	Version int64

	// HydratedAt is when the entity was last hydrated (RFC3339; "" means
	// NULL, never hydrated). New schema only; always "" when read from an
	// old-schema store. Populated by reads; UpsertEntity and
	// WriteEntityWithLog ignore it. WriteEntityStateWithLog sets it.
	HydratedAt string

	// Inactive reports that the entity is no longer active (the active
	// column is 0). It is inverted on purpose: the zero value means ACTIVE,
	// so every existing literal keeps meaning "active". New schema only;
	// always false when read from an old-schema store. Populated by reads;
	// UpsertEntity and WriteEntityWithLog ignore it, so no existing caller
	// can deactivate an entity by accident. WriteEntityStateWithLog sets it.
	Inactive bool

	// ListFP is the list fingerprint observed at the last successful
	// hydration ("" when the column is NULL, and always "" on an old-schema
	// store). Populated by reads; UpsertEntity and WriteEntityWithLog ignore
	// it. WriteEntityStateWithLogFP sets it.
	ListFP string
}

// UpsertEntity inserts or replaces the entity row keyed by
// (Repo, EntityType, EntityID).
func (s *Store) UpsertEntity(e Entity) error {
	_, err := s.sql.Exec(
		`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo, entity_type, entity_id) DO UPDATE SET
		   facts = excluded.facts,
		   as_of = excluded.as_of,
		   stale = excluded.stale,
		   content_hash = excluded.content_hash,
		   head_sha = excluded.head_sha`,
		e.Repo, e.EntityType, e.EntityID, e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA),
	)
	if err != nil {
		return fmt.Errorf("store: upsert entity (%s,%s,%s): %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	return nil
}

// GetEntity returns the entity row for (repo, entityType, entityID), or
// found=false if no such row exists.
func (s *Store) GetEntity(repo, entityType, entityID string) (entity Entity, found bool, err error) {
	cols, err := s.entityNewColumns()
	if err != nil {
		return Entity{}, false, err
	}
	var headSHA, hydratedAt, listFP sql.NullString
	var active int
	row := s.sql.QueryRow(
		`SELECT repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha, `+cols+`
		 FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	)
	if err := row.Scan(&entity.Repo, &entity.EntityType, &entity.EntityID, &entity.Facts,
		&entity.AsOf, &entity.Stale, &entity.ContentHash, &headSHA, &entity.Version, &hydratedAt, &active, &listFP); err != nil {
		if err == sql.ErrNoRows {
			return Entity{}, false, nil
		}
		return Entity{}, false, fmt.Errorf("store: get entity (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	entity.HeadSHA = headSHA.String
	entity.HydratedAt = hydratedAt.String
	entity.Inactive = active == 0
	entity.ListFP = listFP.String
	return entity, true, nil
}

// CountEntities returns the total number of rows in the entity table,
// across every repo/type. Added for packet 8's `pg-desk status`
// [docs/behavior/pg-desk/operator-commands.md: "entity and interpretation
// counts"] — mirrors HasAnyInterpretation's own plain-COUNT pattern
// (interpretation.go) rather than requiring a caller to page through
// ListInterpretations-style rows just to count them.
func (s *Store) CountEntities() (int, error) {
	var n int
	if err := s.sql.QueryRow(`SELECT COUNT(*) FROM entity`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count entities: %w", err)
	}
	return n, nil
}

// ListEntities returns every entity row, ordered by
// (repo, entity_type, entity_id) for a deterministic result — mirrors
// ListInterpretations' own identical ordering rationale (interpretation.go).
// Added for `pg-desk sweep` (bead pg2-gznpe): the bulk-backfill command's
// own "iterate every entity currently in the store" driver reads this list
// rather than requiring a caller to invent its own full-table scan.
func (s *Store) ListEntities() ([]Entity, error) {
	cols, err := s.entityNewColumns()
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(
		`SELECT repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha, ` + cols + `
		 FROM entity ORDER BY repo, entity_type, entity_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list entities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Entity
	for rows.Next() {
		var e Entity
		var headSHA, hydratedAt, listFP sql.NullString
		var active int
		if err := rows.Scan(&e.Repo, &e.EntityType, &e.EntityID, &e.Facts, &e.AsOf, &e.Stale, &e.ContentHash, &headSHA, &e.Version, &hydratedAt, &active, &listFP); err != nil {
			return nil, fmt.Errorf("store: scan entity row: %w", err)
		}
		e.HeadSHA = headSHA.String
		e.HydratedAt = hydratedAt.String
		e.Inactive = active == 0
		e.ListFP = listFP.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list entities: %w", err)
	}
	return out, nil
}

// entityNewColumns returns the SELECT expressions for the new-schema entity
// columns, in the order version, hydrated_at, active, list_fp: the real
// columns on the new schema, the constants 0, NULL, 1, NULL on the old one
// (which has none of them). Called before the query is issued, never while a result set is open.
func (s *Store) entityNewColumns() (string, error) {
	isNew, err := s.isNewSchema()
	if err != nil {
		return "", err
	}
	if isNew {
		return "version, hydrated_at, active, list_fp", nil
	}
	return "0, NULL, 1, NULL", nil
}

// nullableString maps an empty Go string to a SQL NULL, so optional
// TEXT columns (e.g. entity.head_sha) round-trip as NULL rather than "".
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
