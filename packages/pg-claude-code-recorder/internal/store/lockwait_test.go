package store

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A writer queued behind the write lock observes how long it queued, measured
// from before it asked for the lock; an uncontended writer observes nothing.
func TestStoreWriteLockWaitIsObservedBeforeAcquiring(t *testing.T) {
	o := obs.New(nil)
	s, err := Open(t.TempDir(), Options{Obs: o})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Uncontended: no observation.
	if err := s.Commit(&Rec{T: TEpoch, At: 1, Epoch: 99}); err != nil {
		t.Fatal(err)
	}
	if h := o.Reg.Hist(obs.MStoreWriteLockWait); h != nil && h.Samples != 0 {
		t.Fatalf("uncontended commit observed %d waits", h.Samples)
	}

	// A clock that moves 1 s per read: the wait is the gap between the read
	// before asking for the lock and the read after getting it.
	var ticks atomic.Int64
	base := time.Unix(1000, 0)
	s.waitNow = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) }
	queued := make(chan struct{})
	s.onContended = func() { close(queued) }
	s.mu.Lock()
	done := make(chan error)
	go func() { done <- s.Commit(&Rec{T: TEpoch, At: 2, Epoch: 100}) }()
	<-queued
	s.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h := o.Reg.Hist(obs.MStoreWriteLockWait)
	if h == nil || h.Samples != 1 || h.Sum != 1 {
		t.Fatalf("store_write_lock_wait_seconds = %+v, want 1 sample of 1 s", h)
	}
}
