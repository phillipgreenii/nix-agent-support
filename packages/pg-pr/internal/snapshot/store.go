package snapshot

import "sync"

// Store holds the latest Snapshot for the dashboard handler.
// Safe for concurrent access.
type Store struct {
	mu  sync.RWMutex
	cur *Snapshot
}

// NewStore constructs an empty Store. Get returns (nil, false) until Set
// is called.
func NewStore() *Store { return &Store{} }

// Set replaces the held snapshot atomically.
func (s *Store) Set(snap *Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = snap
}

// Get returns the held snapshot, or (nil, false) when none has been set.
func (s *Store) Get() (*Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cur == nil {
		return nil, false
	}
	return s.cur, true
}
