package lock

import (
	"sync"
	"testing"
	"time"
)

func TestFlock_serializesSameName(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)

	var mu sync.Mutex
	overlap := false
	active := false
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := fl.Lock("alpha")
			if err != nil {
				t.Errorf("Lock: %v", err)
				return
			}
			mu.Lock()
			if active {
				overlap = true
			}
			active = true
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			active = false
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if overlap {
		t.Error("two holders of the same-name lock overlapped")
	}
}

func TestFlock_differentNamesDoNotBlock(t *testing.T) {
	fl := New(t.TempDir())
	u1, err := fl.Lock("a")
	if err != nil {
		t.Fatal(err)
	}
	defer u1()
	done := make(chan struct{})
	go func() {
		u2, err := fl.Lock("b") // different name → must not block on "a"
		if err == nil {
			u2()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Lock on a different name blocked")
	}
}
