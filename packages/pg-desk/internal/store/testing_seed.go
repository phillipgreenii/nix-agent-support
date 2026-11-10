package store

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
	if err := s.UpsertEntity(e); err != nil {
		t.Fatalf("SeedEntityForTest: %v", err)
	}
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
