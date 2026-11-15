package eventqueue

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/activity"
)

// This file carries the HARD perf/allocation-budget gates for the
// observability path (Task 3.11 Objective): ordinary TestX functions, run
// under plain `go test` — the same binary the `pg-router-go-tests` flake check
// exercises. bench_test.go's Benchmark* functions cover the same ground
// informationally; `go test` never runs a Benchmark body without `-bench`,
// so a budget assertion placed there would silently never execute under the
// gate (Task 3.11's "Why the split" — the exact vacuous-check failure mode
// bead pg2-3nb2t already burned this workspace on once, there for a
// build-vs-check-attribute confusion rather than a bench-vs-test one, but the
// same shape: the wrong thing was being watched).
//
// TestDispatchOverheadUnderRingReader is the one wall-CLOCK budget, and it
// lives in perf_walltime_test.go behind the `perf` build tag, NOT here: its
// duration and pass/fail threshold are at the mercy of how fast, and how
// loaded, the host is, so (V-4) it is not in the unit gate. The unit gate,
// `pg-router-go-tests` and the commit/pre-land hook runs all agree: none of them
// runs it. It is run by `pg-router-perf-tests`
// (`nix build .#checks.<system>.pg-router-perf-tests`), on demand. The
// allocation-counted budgets below (TestEnqueueAllocBudget /
// TestDispatchAllocBudget; host speed cannot exhaust them) stay in the unit tier
// and keep enforcing the hard observability-path gate.

// ringObserver is the MINIMAL Observer this file needs to measure "the
// observability path": recording straight onto an activity.Ring using the
// type/id already at hand at each hook. It is deliberately simpler than
// cmd/pg-router/run.go's production activityObserver (which layers a
// mutex-guarded eventID->Type correlation map on top, to work around
// OnAccept not carrying an event type) — that extra bookkeeping is core's
// own adapter-level concern, not eventqueue's, and this package has no
// reason to re-measure it here. What this DOES measure is the cost this
// package's own Observer hook points add once something is listening on
// them and turning every hook into a live activity.Ring entry — exactly the
// integration Task 3.4's Ring is built for (its own doc: "meant to be read
// live and directly... not embedded in any periodic snapshot").
type ringObserver struct{ ring *activity.Ring }

func (o *ringObserver) OnEnqueue(evt Event) {
	o.ring.Append(activity.Entry{Type: evt.Type, Outcome: "enqueued"})
}

func (o *ringObserver) OnAccept(eventID, _ string) {
	o.ring.Append(activity.Entry{Type: eventID, Outcome: "delivered"})
}

func (o *ringObserver) OnUnconsumedExpired(evtType string) {
	o.ring.Append(activity.Entry{Type: evtType, Outcome: "missed"})
}

func (o *ringObserver) OnDeclined(evtType, _, _ string) {
	o.ring.Append(activity.Entry{Type: evtType, Outcome: "declined"})
}

func (o *ringObserver) OnDispatchFailure(evtType, _ string) {
	o.ring.Append(activity.Entry{Type: evtType, Outcome: "dispatch_failed"})
}

func (o *ringObserver) OnDeduped(evtType string) {
	o.ring.Append(activity.Entry{Type: evtType, Outcome: "deduped"})
}

// statelessAcceptListener is a Listener with no mutable state of its own —
// unlike this package's queue_test.go fakeListener (which records every
// offer into unsynchronized slices/maps), it is safe to share across
// multiple GOROUTINES calling Dispatch concurrently, which several tests in
// this file do (TestDepthByTypeUnderContention, and the concurrent-reader
// variant of TestDispatchOverheadUnderRingReader's setup).
type statelessAcceptListener struct{ id, typ string }

func (l *statelessAcceptListener) ID() string                 { return l.id }
func (l *statelessAcceptListener) Matches(e Event) bool       { return e.Type == l.typ }
func (l *statelessAcceptListener) Offer(Offering) OfferResult { return OfferResult{Accepted: true} }

// --- Step 1: TestEnqueueAllocBudget / TestDispatchAllocBudget --------------
//
// Both measure the SAME thing on their respective call: the incremental
// allocation cost of wiring full observability — an Observer that turns
// every hook into a live activity.Ring write, the real production shape
// (cmd/pg-router/run.go's bootCore wires exactly this, modulo the adapter
// bookkeeping noted on ringObserver above) — on top of the queue's own
// baseline bookkeeping cost for the same call. Ring.Append itself is
// proven zero-allocation (internal/activity's TestAppendZeroAllocs), so
// this is the gate that keeps that promise from being silently defeated by
// however the ring ends up wired into Enqueue/Dispatch.
//
// It is NOT a budget on Enqueue/Dispatch's own total allocation cost — that
// cost is dominated by durable bookkeeping (Store.Append, the entry's
// accepted/settled maps, the FIFO order slice, the depth cell's
// copy-on-write publish) that has nothing to do with observability and that
// this task's Contract does not ask this packet to bound. Measured
// directly, steady-state Enqueue runs ~8 allocs/event and steady-state
// Dispatch (with early eviction) ~18 allocs/event on this implementation;
// asserting either of those totals at <=2 would fail, and pin this
// currently-landed durable/FIFO bookkeeping shape as a red-first
// regression -- not the observability-overhead question Task 3.11
// Objective actually asks with "the observability path".

// enqueueAllocs measures steady-state per-event Enqueue allocations for
// obs, so TestEnqueueAllocBudget can compare wired-observer against
// noopObserver's baseline. Every call uses a FRESH id (via seq) so no run
// hits Enqueue's stale-retire branch, which is a different, heavier code
// path than the steady-state new-id one this budget is about.
func enqueueAllocs(t *testing.T, obs Observer, runs int, prefix string) float64 {
	t.Helper()
	q := newQueue(t, newClock(), WithObserver(obs))
	i := 0
	return testing.AllocsPerRun(runs, func() {
		i++
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("%s%d", prefix, i), Type: "T"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	})
}

// TestEnqueueAllocBudget is the Step 1 red-first test: run once (as it is
// here) to confirm the observability delta is within budget on the
// currently-landed Tasks 3.1/3.2 implementation.
func TestEnqueueAllocBudget(t *testing.T) {
	const runs = 2000
	const budget = 2.0

	baseline := enqueueAllocs(t, noopObserver{}, runs, "b")
	withRing := enqueueAllocs(t, &ringObserver{ring: activity.New(activity.DefaultSize)}, runs, "o")
	delta := withRing - baseline

	t.Logf("Enqueue allocs/event: baseline=%v withRingObserver=%v delta=%v", baseline, withRing, delta)
	if delta > budget {
		t.Fatalf("Enqueue observability-path delta = %v allocs/event, want <= %v (baseline=%v, withRingObserver=%v)",
			delta, budget, baseline, withRing)
	}
}

// dispatchAllocs measures steady-state per-event Dispatch allocations for
// obs: n events are pre-enqueued (untimed setup, matching Enqueue's own
// budget being asserted separately rather than folded in here), early
// eviction is on so the queue does not grow without bound across the n
// measured Dispatch calls, and the listener is the stateless accepting one
// so every call actually does the accept/evict work Dispatch's phase 3
// exercises.
func dispatchAllocs(t *testing.T, obs Observer, n int, idPrefix string) float64 {
	t.Helper()
	q := newQueue(t, newClock(), WithEarlyEviction(), WithObserver(obs))
	q.Register(&statelessAcceptListener{id: "h", typ: "T"})
	for i := 0; i < n; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("%s%d", idPrefix, i), Type: "T"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	return testing.AllocsPerRun(n, func() {
		q.Dispatch()
	})
}

// TestDispatchAllocBudget is TestEnqueueAllocBudget's Dispatch-side twin
// (same red-first note applies).
func TestDispatchAllocBudget(t *testing.T) {
	const n = 2000
	const budget = 2.0

	baseline := dispatchAllocs(t, noopObserver{}, n, "b")
	withRing := dispatchAllocs(t, &ringObserver{ring: activity.New(activity.DefaultSize)}, n, "o")
	delta := withRing - baseline

	t.Logf("Dispatch allocs/event: baseline=%v withRingObserver=%v delta=%v", baseline, withRing, delta)
	if delta > budget {
		t.Fatalf("Dispatch observability-path delta = %v allocs/event, want <= %v (baseline=%v, withRingObserver=%v)",
			delta, budget, baseline, withRing)
	}
}

// --- Step 3: TestDepthByTypeUnderContention --------------------------------

// TestDepthByTypeUnderContention proves the lock-free read itself (Task
// 3.2's depthCell, not the ordinary Dispatch/Expire lock contention on q.mu)
// stays fast at 10k retained entries even while other goroutines are
// concurrently mutating the queue (Enqueue/Dispatch/Expire, each taking
// q.mu) — DepthByType never takes q.mu (its own doc: "cannot self-deadlock
// even if a future caller reached it while already holding q.mu"), so its
// own latency should be near-constant regardless of how busy the mutation
// side is.
func TestDepthByTypeUnderContention(t *testing.T) {
	const entries = 10000
	const p99Budget = 100 * time.Microsecond

	q, err := New(NewMemStore())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q.Register(&statelessAcceptListener{id: "h", typ: "T"})
	farFuture := time.Now().Add(time.Hour)
	for i := 0; i < entries; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", i), Type: "T", ExpiresAt: farFuture}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	const writers = 4
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			i := 0
			for !stop.Load() {
				id := fmt.Sprintf("w%d-%d", w, i)
				i++
				if _, err := q.Enqueue(Event{ID: id, Type: "T", ExpiresAt: farFuture}); err != nil {
					return
				}
				q.Dispatch()
				q.Expire()
			}
		}(w)
	}

	const samples = 5000
	lat := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		_ = q.DepthByType()
		lat[i] = time.Since(start)
	}
	stop.Store(true)
	wg.Wait()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99 := lat[int(float64(len(lat))*0.99)]
	t.Logf("DepthByType p99 under contention (>= %d retained entries) = %v (max=%v)", entries, p99, lat[len(lat)-1])
	if p99 >= p99Budget {
		t.Fatalf("DepthByType p99 under contention = %v, want < %v", p99, p99Budget)
	}
}
