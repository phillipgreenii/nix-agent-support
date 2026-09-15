package kvstore

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentGetPutDelete exercises get/put/delete concurrently across many
// goroutines and keys. Run with -race: the inMemoryStore's sync.RWMutex must
// make every operation safe for concurrent use.
func TestConcurrentGetPutDelete(t *testing.T) {
	s := NewInMemory()

	const goroutines = 16
	const keys = 8
	const iterations = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("key-%d", (g+i)%keys)
				switch i % 3 {
				case 0:
					if err := s.Put(key, fmt.Sprintf("g%d-i%d", g, i)); err != nil {
						t.Errorf("Put: unexpected error: %v", err)
					}
				case 1:
					if _, _, err := s.Get(key); err != nil {
						t.Errorf("Get: unexpected error: %v", err)
					}
				case 2:
					if err := s.Delete(key); err != nil {
						t.Errorf("Delete: unexpected error: %v", err)
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestNewInMemoryStartsEmpty documents that a freshly constructed store has
// no keys until Put is called.
func TestNewInMemoryStartsEmpty(t *testing.T) {
	s := NewInMemory()

	_, ok, err := s.Get("anything")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("Get: expected a fresh store to have no keys")
	}
}
