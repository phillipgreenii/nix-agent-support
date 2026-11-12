package store

import (
	"context"
	"fmt"
	"path/filepath"
)

// Test-only seeding helpers. Production code writes an interpretation only
// through WriteInterpretation, which needs an entity row and a schema-2 store;
// a test that needs a row in place without running a hydration seeds it with
// these. They are named ForTest and fail the test when the store is not on
// schema 2, so a test cannot silently seed a store the production path would
// refuse.

// seedT is the part of *testing.T the helpers use.
type seedT interface {
	Helper()
	Fatalf(string, ...any)
}

// SeedEntityForTest writes (or replaces) the entity row e on a schema-2 store
// without touching version, hydrated_at or active.
func (s *Store) SeedEntityForTest(t seedT, e Entity) {
	t.Helper()
	if err := s.RequireNewSchema(); err != nil {
		t.Fatalf("SeedEntityForTest: %v", err)
	}
	if err := s.UpsertEntityForTest(e); err != nil {
		t.Fatalf("SeedEntityForTest: %v", err)
	}
}

// UpsertEntityForTest is SeedEntityForTest for a caller that wants the error
// back: it inserts or replaces the entity row keyed by
// (Repo, EntityType, EntityID) without touching version, hydrated_at or
// active. A first insert stamps first_seen_at with e.AsOf and the
// update branch never rewrites it.
func (s *Store) UpsertEntityForTest(e Entity) error {
	_, err := s.sql.ExecContext(
		context.Background(),
		`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha, first_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo, entity_type, entity_id) DO UPDATE SET
		   facts = excluded.facts,
		   as_of = excluded.as_of,
		   stale = excluded.stale,
		   content_hash = excluded.content_hash,
		   head_sha = excluded.head_sha`,
		e.Repo, e.EntityType, e.EntityID, e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA), nullableString(e.AsOf),
	)
	if err != nil {
		return fmt.Errorf("store: seed entity (%s,%s,%s): %w", e.Repo, e.EntityType, e.EntityID, err)
	}
	return nil
}

// SeedInterpretationForTest writes (or replaces) the interpretation row i on a
// schema-2 store with the handle's stamp, without looking at the entity.
func (s *Store) SeedInterpretationForTest(t seedT, i Interpretation) {
	t.Helper()
	if err := s.RequireNewSchema(); err != nil {
		t.Fatalf("SeedInterpretationForTest: %v", err)
	}
	if err := s.UpsertInterpretation(i); err != nil {
		t.Fatalf("SeedInterpretationForTest: %v", err)
	}
}

// SeedEntityAndInterpretationForTest seeds both rows; the entity's
// content_hash is what a WriteInterpretation must expect.
func (s *Store) SeedEntityAndInterpretationForTest(t seedT, e Entity, i Interpretation) {
	t.Helper()
	s.SeedEntityForTest(t, e)
	s.SeedInterpretationForTest(t, i)
}

// OpenRetiredSchemaForTest opens, with OpenRaw, an in-temp-dir store that
// reports the retired schema version 1 and holds only meta and a version-1
// interpretation table (no stamp column, so a test can seed a row and watch a
// reader degrade). It is
// for the tests that prove a command refuses a store that has not been brought
// to schema 2 (RequireNewSchema, ErrOldSchema); Open itself refuses such a
// store, so only a raw handle can reach it.
func OpenRetiredSchemaForTest(t testT) *Store {
	s, err := OpenRaw(filepath.Join(t.TempDir(), "retired.db"))
	if err != nil {
		t.Fatalf("OpenRetiredSchemaForTest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE interpretation (
    repo             TEXT NOT NULL,
    entity_type      TEXT NOT NULL,
    entity_id        TEXT NOT NULL,
    ownership        TEXT,
    enrichment       TEXT,
    urgency          TEXT,
    category         TEXT,
    dispositions     TEXT,
    approvals        TEXT,
    gate_state       TEXT,
    match_reasons    TEXT,
    panel            TEXT,
    ready_to_promote INTEGER NOT NULL DEFAULT 0,
    degraded         INTEGER NOT NULL DEFAULT 0,
    as_of            TEXT NOT NULL,
    PRIMARY KEY (repo, entity_type, entity_id)
)`,
		`INSERT INTO meta (key, value) VALUES ('schema_version', '1')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := s.sql.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("OpenRetiredSchemaForTest: %v", err)
		}
	}
	return s
}
