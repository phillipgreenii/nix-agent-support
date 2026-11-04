package daemon

import (
	"sync"
	"testing"
)

// --- tracker ---

func TestTracker_AddResolveDeliversOutcome(t *testing.T) {
	tr := newTracker()
	ch := tr.add("id-1", 100)

	tr.resolve("id-1", true, "", "", false)

	select {
	case o := <-ch:
		if !o.ok {
			t.Fatalf("outcome.ok = false, want true")
		}
		if o.err != "" {
			t.Fatalf("outcome.err = %q, want empty", o.err)
		}
	default:
		t.Fatal("expected outcome to be available after resolve")
	}
}

func TestTracker_ResolveWithError(t *testing.T) {
	tr := newTracker()
	ch := tr.add("id-1", 100)

	tr.resolve("id-1", false, "boom", "", false)

	o := <-ch
	if o.ok {
		t.Fatalf("outcome.ok = true, want false")
	}
	if o.err != "boom" {
		t.Fatalf("outcome.err = %q, want %q", o.err, "boom")
	}
}

func TestTracker_ResolveUnknownID_NoPanic(t *testing.T) {
	tr := newTracker()
	// Resolving an id that was never added (or already resolved/cancelled)
	// must be a safe no-op, not a panic or a send on a closed channel.
	tr.resolve("nonexistent", true, "", "", false)
}

func TestTracker_FailServer_OnlyThatServer(t *testing.T) {
	tr := newTracker()
	chA := tr.add("id-a", 100)
	chB := tr.add("id-b", 200)

	tr.failServer(100)

	select {
	case o := <-chA:
		if o.ok {
			t.Fatalf("failServer(100): id-a outcome.ok = true, want false")
		}
	default:
		t.Fatal("expected id-a to be failed by failServer(100)")
	}

	select {
	case <-chB:
		t.Fatal("id-b belongs to a different server PID and must not be failed")
	default:
		// Still pending, as expected.
	}

	// id-b's server can still be resolved normally afterwards.
	tr.resolve("id-b", true, "", "", false)
	o := <-chB
	if !o.ok {
		t.Fatalf("id-b outcome.ok = false, want true")
	}
}

func TestTracker_Cancel_RemovesPendingWithoutSend(t *testing.T) {
	tr := newTracker()
	ch := tr.add("id-1", 100)
	tr.cancel("id-1")

	// A later resolve for the same id must be a no-op (already cancelled).
	tr.resolve("id-1", true, "", "", false)

	select {
	case <-ch:
		t.Fatal("cancelled id must not receive an outcome")
	default:
	}
}

func TestTracker_ConcurrentAddResolve(t *testing.T) {
	tr := newTracker()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id := tr.nextID()
			ch := tr.add(id, i%3)
			go tr.resolve(id, true, "", "", false)
			<-ch
		}(i)
	}
	wg.Wait()
}
