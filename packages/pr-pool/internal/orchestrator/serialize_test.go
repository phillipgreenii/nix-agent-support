package orchestrator

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// --- Task 3.9's concurrency-test-matrix: slow-serialization progress -------
//
// This file lives in package orchestrator rather than internal/eventqueue
// (where INV-CONC-1's own per-type occupancy mechanics — WithSerializeTypes,
// headFor's `occupied` bookkeeping — are already thoroughly covered by that
// package's own serialize_test.go) because the guarantee this test proves is
// the one that matters at the production integration point: a real
// deployment's config-declared [pool].serialize_types (bootCore's
// eventqueue.WithSerializeTypes(cfg.SerializeTypes...), cmd/pr-pool/run.go)
// wires a queue shared by every configured role's roleListener, and a slow
// role on one serialize-marked type must never globally throttle dispatch
// for an unrelated role/type sharing that same queue. It exercises only
// eventqueue's already-PUBLIC API (Queue, Listener, WithSerializeTypes),
// with a small package-local fake Listener — an equivalent of eventqueue's
// own unexported test double, needed here because that one is
// package-private and this file lives in package orchestrator.

// fakeQueueListener is a minimal eventqueue.Listener double: it either
// always declines (busy) — the perpetually-occupying "slow" role — or
// always accepts, recording every event id it was offered.
type fakeQueueListener struct {
	id   string
	typ  string
	slow bool // never accepts; always DeclineBusy

	mu       sync.Mutex
	accepted []string
}

func (l *fakeQueueListener) ID() string                      { return l.id }
func (l *fakeQueueListener) Matches(e eventqueue.Event) bool { return e.Type == l.typ }

func (l *fakeQueueListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	if l.slow {
		return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy}
	}
	l.mu.Lock()
	l.accepted = append(l.accepted, o.Event.ID)
	l.mu.Unlock()
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

func (l *fakeQueueListener) acceptedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.accepted)
}

// TestSlowSerializedHeadDoesNotStallUnrelatedTypeDispatch proves the
// per-type serialize occupancy gate (INV-CONC-1) never throttles dispatch
// for an UNRELATED type: a serialize-marked type held perpetually occupied
// by a slow listener that never accepts must not stall a second, unrelated
// type's own listener from making steady progress across repeated Dispatch
// passes on the SAME shared queue [design: Task 3.9 Acceptance —
// "a serialize-marked head held by a slow listener does not stall
// unrelated types' dispatch"].
func TestSlowSerializedHeadDoesNotStallUnrelatedTypeDispatch(t *testing.T) {
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithSerializeTypes("gated-type"))
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}

	slow := &fakeQueueListener{id: "slow", typ: "gated-type", slow: true}
	fast := &fakeQueueListener{id: "fast", typ: "unrelated-type"}
	q.Register(slow)
	q.Register(fast)

	// The serialize-marked type's one entry occupies its slot FOREVER: slow
	// never accepts it and it never expires, so releasedLocked never fires.
	if _, err := q.Enqueue(eventqueue.Event{ID: "g1", Type: "gated-type", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("enqueue gated-type occupant: %v", err)
	}

	const n = 50
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("u%d", i)
		if _, err := q.Enqueue(eventqueue.Event{ID: id, Type: "unrelated-type", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("enqueue unrelated-type event %s: %v", id, err)
		}
		q.Dispatch()
	}

	if got := fast.acceptedCount(); got != n {
		t.Fatalf("fast accepted %d of %d unrelated-type events, want all %d — a perpetually-occupied serialize-marked type must not stall an unrelated type's dispatch", got, n, n)
	}
	if got := slow.acceptedCount(); got != 0 {
		t.Fatalf("slow accepted %d, want 0 (it never accepts by construction)", got)
	}
}
