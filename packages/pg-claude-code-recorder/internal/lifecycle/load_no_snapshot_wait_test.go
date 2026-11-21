package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A model load queued behind the registry cache holder MUST NOT hold a read
// snapshot while it waits: the holder may be in a slow read, and a waiter that
// pinned a reader connection would starve the pool behind it.
func TestLoadWaitsForTheRegistryCacheWithoutAReadSnapshot(t *testing.T) {
	v := newLiveEnv(t)
	if err := v.e.regMu.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			v.e.regMu.Unlock()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := v.e.loadWith(ctx, false)
		done <- err
	}()

	// While the cache is held the load is queued: it must not be in a snapshot.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := v.e.St.PoolStats().Read.InUse; n != 0 {
			t.Fatalf("a load waiting for the registry cache holds %d read connections", n)
		}
		select {
		case err := <-done:
			t.Fatalf("the load finished while the registry cache was held: %v", err)
		case <-time.After(time.Millisecond):
		}
	}

	cancel() // a cancelled waiter leaves the queue
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load returned %v, want context.Canceled", err)
	}
	if n := v.e.St.PoolStats().Read.InUse; n != 0 {
		t.Fatalf("read connections in use after the cancelled load: %d", n)
	}

	v.e.regMu.Unlock()
	held = false
	if _, err := v.e.loadWith(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if n := v.e.St.PoolStats().Read.InUse; n != 0 {
		t.Fatalf("read connections in use after a load: %d", n)
	}
}
