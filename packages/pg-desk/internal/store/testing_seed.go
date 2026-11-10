package store

import "fmt"

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
// back and for a store on either schema (the tests of the old schema and of
// the cutover seed with it): it inserts or replaces the entity row keyed by
// (Repo, EntityType, EntityID) without touching version, hydrated_at or
// active. On schema 2 a first insert stamps first_seen_at with e.AsOf and the
// update branch never rewrites it.
func (s *Store) UpsertEntityForTest(e Entity) error {
	isNew, err := s.isNewSchema()
	if err != nil {
		return err
	}
	cols, marks := `repo, entity_type, entity_id, facts, as_of, stale, content_hash, head_sha`, `?, ?, ?, ?, ?, ?, ?, ?`
	args := []any{e.Repo, e.EntityType, e.EntityID, e.Facts, e.AsOf, e.Stale, e.ContentHash, nullableString(e.HeadSHA)}
	if isNew {
		cols += `, first_seen_at`
		marks += `, ?`
		args = append(args, nullableString(e.AsOf))
	}
	_, err = s.sql.Exec(
		`INSERT INTO entity (`+cols+`) VALUES (`+marks+`)
		 ON CONFLICT (repo, entity_type, entity_id) DO UPDATE SET
		   facts = excluded.facts,
		   as_of = excluded.as_of,
		   stale = excluded.stale,
		   content_hash = excluded.content_hash,
		   head_sha = excluded.head_sha`,
		args...,
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
