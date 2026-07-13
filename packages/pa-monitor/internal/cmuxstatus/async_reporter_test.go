package cmuxstatus

import (
	"sync"
	"testing"
	"time"
)

// blockingReporter is a controllable inner Reporter: each Push announces itself
// on started, blocks until release is closed, then records the snapshot and
// announces completion on painted. This lets a test hold the worker inside a
// paint and then deterministically wait for a paint to finish (via painted)
// without racing the wrapper's teardown.
type blockingReporter struct {
	started chan struct{}
	painted chan struct{}
	release chan struct{}

	mu      sync.Mutex
	pushes  []Snapshot
	cleared int
}

func newBlockingReporter() *blockingReporter {
	return &blockingReporter{
		started: make(chan struct{}, 8),
		painted: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
}

func (b *blockingReporter) Push(s Snapshot) {
	b.started <- struct{}{}
	<-b.release
	b.mu.Lock()
	b.pushes = append(b.pushes, s)
	b.mu.Unlock()
	b.painted <- struct{}{}
}

func (b *blockingReporter) Notify(string, string) {}

func (b *blockingReporter) Clear() {
	b.mu.Lock()
	b.cleared++
	b.mu.Unlock()
}

func (b *blockingReporter) snapshots() []Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Snapshot(nil), b.pushes...)
}

func (b *blockingReporter) clears() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cleared
}

// TestAsyncReporterPushDoesNotBlockWhilePainting is the whole point of W1: a
// Push must return immediately even while the worker is stuck inside a slow
// paint (the hung `cmux` CLI under load), so the caller (the bridge gRPC
// receive loop) is never blocked.
func TestAsyncReporterPushDoesNotBlockWhilePainting(t *testing.T) {
	inner := newBlockingReporter()
	a := newAsyncReporter(inner)
	defer func() { close(inner.release); a.Clear() }()

	a.Push(Snapshot{State: StateWorking})
	<-inner.started // worker is now blocked inside inner.Push

	done := make(chan struct{})
	go func() {
		a.Push(Snapshot{State: StateIdle})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Push blocked while a paint was in flight")
	}
}

// TestAsyncReporterCoalescesToLatest verifies latest-wins: bursts enqueued while
// a paint is in flight collapse to the single newest snapshot, so a slow painter
// never has to work through a backlog.
func TestAsyncReporterCoalescesToLatest(t *testing.T) {
	inner := newBlockingReporter()
	a := newAsyncReporter(inner)
	defer a.Clear()

	a.Push(Snapshot{Progress: 0.1, HasProgress: true})
	<-inner.started // worker blocked painting 0.1

	// These three enqueue while the worker is stuck on 0.1, so they collapse to
	// the newest (0.9) in the depth-1 buffer.
	a.Push(Snapshot{Progress: 0.2, HasProgress: true})
	a.Push(Snapshot{Progress: 0.3, HasProgress: true})
	a.Push(Snapshot{Progress: 0.9, HasProgress: true})

	close(inner.release)
	<-inner.painted // 0.1 finished; worker now drains the coalesced 0.9
	<-inner.started // worker began painting 0.9
	<-inner.painted // 0.9 finished — both records are now visible

	got := inner.snapshots()
	if len(got) != 2 {
		t.Fatalf("expected 2 paints (first + coalesced latest), got %d: %+v", len(got), got)
	}
	if got[0].Progress != 0.1 || got[1].Progress != 0.9 {
		t.Errorf("expected paints [0.1, 0.9], got [%v, %v]", got[0].Progress, got[1].Progress)
	}
}

// TestAsyncReporterClearJoinsAndIsIdempotent verifies Clear waits for the
// in-flight paint to finish before issuing the inner Clear (no concurrent access
// to the inner's single-writer state), and that repeat Clears are no-ops.
func TestAsyncReporterClearJoinsAndIsIdempotent(t *testing.T) {
	inner := newBlockingReporter()
	a := newAsyncReporter(inner)

	a.Push(Snapshot{})
	<-inner.started
	close(inner.release)

	a.Clear()
	if got := inner.clears(); got != 1 {
		t.Fatalf("after Clear want 1 inner Clear, got %d", got)
	}
	a.Clear() // idempotent
	if got := inner.clears(); got != 1 {
		t.Fatalf("second Clear must be a no-op, got %d inner Clears", got)
	}
	// Push after Clear is dropped, not a panic/deadlock.
	a.Push(Snapshot{})
}
