package store

// UpsertEntity is the name the store package's own tests seed entities under.
func (s *Store) UpsertEntity(e Entity) error { return s.UpsertEntityForTest(e) }
