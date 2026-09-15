package kvstore

import "sync"

// inMemoryStore is a sync.RWMutex-guarded map[string]string implementation
// of Store. It holds no durability of its own — state is lost on process
// restart.
type inMemoryStore struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewInMemory returns a Store backed by an in-memory map, safe for
// concurrent use.
func NewInMemory() Store {
	return &inMemoryStore{data: make(map[string]string)}
}

func (s *inMemoryStore) Get(key string) (value string, ok bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok = s.data[key]
	return value, ok, nil
}

func (s *inMemoryStore) Put(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *inMemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key) // no-op if key is absent
	return nil
}
