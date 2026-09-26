package store

import (
	"database/sql"
	"fmt"
)

// Entity is one row of the entity table: the last gathered facts for a
// single (repo, entity_type, entity_id), as JSON, per the design doc's
// section 7.6. Written by the gather stage (a later packet in this
// docket); this packet exposes the writer/reader only.
type Entity struct {
	Repo        string
	EntityType  string
	EntityID    string
	Facts       string // JSON blob of last-gathered facts
	AsOf        string // RFC3339 timestamp
	Stale       bool
	ContentHash string
	HeadSHA     string // set for files/commits only; empty otherwise
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
	var headSHA sql.NullString
	row := s.sql.QueryRow(
		`SELECT repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha
		 FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	)
	if err := row.Scan(&entity.Repo, &entity.EntityType, &entity.EntityID, &entity.Facts,
		&entity.AsOf, &entity.Stale, &entity.ContentHash, &headSHA); err != nil {
		if err == sql.ErrNoRows {
			return Entity{}, false, nil
		}
		return Entity{}, false, fmt.Errorf("store: get entity (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	entity.HeadSHA = headSHA.String
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
	rows, err := s.sql.Query(
		`SELECT repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha
		 FROM entity ORDER BY repo, entity_type, entity_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list entities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Entity
	for rows.Next() {
		var e Entity
		var headSHA sql.NullString
		if err := rows.Scan(&e.Repo, &e.EntityType, &e.EntityID, &e.Facts, &e.AsOf, &e.Stale, &e.ContentHash, &headSHA); err != nil {
			return nil, fmt.Errorf("store: scan entity row: %w", err)
		}
		e.HeadSHA = headSHA.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list entities: %w", err)
	}
	return out, nil
}

// nullableString maps an empty Go string to a SQL NULL, so optional
// TEXT columns (e.g. entity.head_sha) round-trip as NULL rather than "".
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
