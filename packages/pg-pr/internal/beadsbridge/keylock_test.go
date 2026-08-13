package beadsbridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestKeyedLockExcludesSameKey pins the mutual-exclusion property the projection
// depends on: while one goroutine holds a key, a second cannot enter.
func TestKeyedLockExcludesSameKey(t *testing.T) {
	k := newKeyedLock()
	release, err := k.acquire(context.Background(), "o/r#1")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	entered := make(chan struct{})
	go func() {
		rel2, err2 := k.acquire(context.Background(), "o/r#1")
		if err2 != nil {
			return
		}
		close(entered)
		rel2()
	}()

	select {
	case <-entered:
		t.Fatal("second acquire of the SAME key entered while the first still held it")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never entered after the first released")
	}
}

// TestKeyedLockAllowsDifferentKeysConcurrently pins the THROUGHPUT property: the
// two daemon workers must stay concurrent for DIFFERENT PRs. A global lock would
// fail this test, which is why it exists alongside the exclusion test.
func TestKeyedLockAllowsDifferentKeysConcurrently(t *testing.T) {
	k := newKeyedLock()
	relA, err := k.acquire(context.Background(), "o/r#1")
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	defer relA()

	done := make(chan struct{})
	go func() {
		relB, err2 := k.acquire(context.Background(), "o/r#2")
		if err2 != nil {
			return
		}
		relB()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a DIFFERENT key blocked behind o/r#1 — the lock is serializing globally")
	}
}

// TestKeyedLockAcquireIsCancellable pins that a waiter abandons on ctx
// cancellation rather than blocking until the holder's bd calls finish. A
// sync.Mutex-based gate cannot satisfy this.
func TestKeyedLockAcquireIsCancellable(t *testing.T) {
	k := newKeyedLock()
	release, err := k.acquire(context.Background(), "o/r#1")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		rel2, err2 := k.acquire(ctx, "o/r#1")
		if rel2 != nil {
			rel2()
			result <- errors.New("acquire returned a release func despite cancellation")
			return
		}
		result <- err2
	}()
	cancel()

	select {
	case got := <-result:
		if !errors.Is(got, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled acquire never returned — the wait is not cancellable")
	}
}

// TestKeyedLockReclaimsSlots pins the bound on the map: keys come from an
// unbounded upstream (every PR the daemon ever sees), so a slot that outlived its
// last holder would be a leak in a process that runs for weeks.
func TestKeyedLockReclaimsSlots(t *testing.T) {
	k := newKeyedLock()
	for i := 0; i < 200; i++ {
		release, err := k.acquire(context.Background(), "o/r#"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		release()
	}
	if got := k.tracked(); got != 0 {
		t.Fatalf("after 200 acquire/release cycles the lock still tracks %d key(s); want 0", got)
	}
}

// TestKeyedLockSerializesContenders drives many goroutines through one key and
// asserts the critical section was never occupied by two at once. Run under
// -race this also proves the unguarded counter is safe only because of the lock.
func TestKeyedLockSerializesContenders(t *testing.T) {
	k := newKeyedLock()
	const goroutines = 32

	var (
		mu       sync.Mutex
		inside   int
		overlaps int
		total    int
	)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := k.acquire(context.Background(), "o/r#7")
			if err != nil {
				return
			}
			defer release()
			mu.Lock()
			inside++
			if inside > 1 {
				overlaps++
			}
			total++
			mu.Unlock()
			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if overlaps != 0 {
		t.Fatalf("%d overlapping entries into the critical section; want 0", overlaps)
	}
	if total != goroutines {
		t.Fatalf("only %d of %d goroutines entered the critical section", total, goroutines)
	}
	if got := k.tracked(); got != 0 {
		t.Fatalf("slot for the contended key was not reclaimed: tracked=%d", got)
	}
}
