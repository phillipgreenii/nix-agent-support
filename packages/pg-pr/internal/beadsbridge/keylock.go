package beadsbridge

import (
	"context"
	"sync"
)

// keyedLock serializes work PER IDENTITY KEY: two goroutines holding different
// keys run concurrently, two holding the same key do not. It exists because
// every bead projection in this package is a check-then-create — read the
// current state, decide, then write — and NOTHING at the bd layer rejects the
// second write (see the Handle doc). Two goroutines interleaved inside that
// window both observe "no bead yet" and both create one, which is the duplicate
// merge-request / process-feedback bead defect (bead pg2-35rl6).
//
// Two properties are load-bearing:
//
//   - Acquisition is CANCELLABLE. The gate is a 1-capacity channel rather than a
//     sync.Mutex because sync.Mutex.Lock cannot be abandoned: a projection whose
//     ctx is cancelled while it waits would block until the holder's bd
//     subprocesses finished, past the shutdown the ctx signalled.
//   - The map is RECLAIMED. Keys are (repo, pr_number) pairs drawn from an
//     unbounded upstream, so a permanent entry per key would grow without bound
//     over a long-lived daemon. Each slot is refcounted by holders PLUS waiters
//     and deleted when the count reaches zero, so the map's size is bounded by
//     the number of projections IN FLIGHT (at most one per drainer goroutine),
//     not by the number of PRs ever seen.
type keyedLock struct {
	mu    sync.Mutex
	slots map[string]*keySlot
}

// keySlot is one key's gate plus its liveness refcount.
type keySlot struct {
	// gate is a 1-capacity semaphore: a successful send acquires, a receive
	// releases. Capacity 1 makes it mutual exclusion.
	gate chan struct{}
	// refs counts the goroutines that hold the gate or are waiting for it. It is
	// guarded by keyedLock.mu, never by the gate itself.
	refs int
}

func newKeyedLock() *keyedLock { return &keyedLock{slots: map[string]*keySlot{}} }

// acquire takes the lock for key and returns the release func. It blocks until
// the key is free or ctx is done; on cancellation it returns ctx.Err() and a nil
// release (the caller MUST NOT release what it never took).
//
// Callers MUST NOT hold two keys at once and MUST NOT re-acquire the same key
// inside a held section: the gate is not reentrant, so either would deadlock.
// The single call site (Handle) takes exactly one key for the whole projection
// and never nests, which is why one flat level is sufficient here.
func (k *keyedLock) acquire(ctx context.Context, key string) (release func(), err error) {
	k.mu.Lock()
	s, ok := k.slots[key]
	if !ok {
		s = &keySlot{gate: make(chan struct{}, 1)}
		k.slots[key] = s
	}
	// Count this goroutine BEFORE it blocks, so the slot it is waiting on cannot
	// be deleted (and replaced by a fresh, already-free one) underneath it.
	s.refs++
	k.mu.Unlock()

	drop := func() {
		k.mu.Lock()
		s.refs--
		if s.refs == 0 {
			delete(k.slots, key)
		}
		k.mu.Unlock()
	}

	select {
	case s.gate <- struct{}{}:
		return func() {
			// ORDER IS LOAD-BEARING: free the gate FIRST, then drop the refcount.
			// Dropping first could take refs to 0 and delete the slot while our
			// token is still in its gate; the next arrival would then build a
			// fresh, free slot and run concurrently with us — the very
			// interleaving this type exists to prevent. Freeing first is safe
			// because any waiter already counted itself, so refs cannot reach 0
			// while one exists.
			<-s.gate
			drop()
		}, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// tracked reports how many keys currently have a slot. Test-only accessor for
// the reclamation invariant.
func (k *keyedLock) tracked() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.slots)
}
