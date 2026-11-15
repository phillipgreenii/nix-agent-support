package store

import "context"

// UpsertEntity is the name the store package's own tests seed entities under.
func (s *Store) UpsertEntity(e Entity) error { return s.UpsertEntityForTest(context.Background(), e) }
