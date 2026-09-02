package eventqueue

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/activity"
)

// This file carries the HARD perf/allocation-budget gates for the
// observability path (Task 3.11 Objective): ordinary TestX functions, run
// under plain `go test` — the same binary the `pr-pool-go-tests` flake check
// exercises. bench_test.go's Benchmark* functions cover the same ground
// informationally; `go test` never runs a Benchmark body without `-bench`,
// so a budget assertion placed there would silently never execute under the
// gate (Task 3.11's "Why the split" — the exact vacuous-check failure mode
// bead pg2-3nb2t already burned this workspace on once, there for a
// build-vs-check-attribute confusion rather than a bench-vs-test one, but the
// same shape: the wrong thing was being watched).

// ringObserver is the MINIMAL Observer this file needs to measure "the
// observability path": recording straight onto an activity.Ring using the
// type/id already at hand at each hook. It is deliberately simpler than
// cmd/pr-pool/run.go's production activityObserver (which layers a
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

func (o *ringObserver) OnDeclined(evtType string) {
	o.ring.Append(activity.Entry{Type: evtType, Outcome: "declined"})
}

func (o *ringObserver) OnDispatchFailure(evtType string) {
	o.ring.Append(activity.Entry{Type: evtType, Outcome: "dispatch_failed"})
}

// statelessAcceptListener is a Listener with no mutable state of its own —
// unlike this package's queue_test.go fakeListener (which records every
// offer into unsynchronized slices/maps), it is safe to share across
// multiple GOROUTINES calling Dispatch concurrently, which several tests in
// this file do (TestDepthByTypeUnderContention, and the concurrent-reader
// variant of TestDispatchOverheadUnderRingReader's setup).
type statelessAcceptListener struct{ id, typ string }

func (l *statelessAcceptListener) ID() string           { return l.id }
func (l *statelessAcceptListener) Matches(e Event) bool { return e.Type == l.typ }
func (l *statelessAcceptListener) Offer(Event) bool     { return true }

// --- Step 1: TestEnqueueAllocBudget / TestDispatchAllocBudget --------------
//
// Both measure the SAME thing on their respective call: the incremental
// allocation cost of wiring full observability — an Observer that turns
// every hook into a live activity.Ring write, the real production shape
// (cmd/pr-pool/run.go's bootCore wires exactly this, modulo the adapter
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

// --- Step 1: TestDispatchOverheadUnderRingReader ---------------------------

// dispatchWallTime runs n Enqueue+Dispatch pairs against a fresh queue
// (early eviction on, so the queue stays bounded) built with obs, and
// returns the wall-clock time of the n Dispatch calls alone (the Enqueue
// setup loop runs before the clock starts). When readerHz > 0, a goroutine
// concurrently calls ring.Read at that cadence for the duration of the
// measured Dispatch loop, standing in for Task 4.0's TUI polling `status`
// (which reads the ring live, per its own doc) while the drive loop is
// mid-pass.
func dispatchWallTime(t *testing.T, n int, obs Observer, readerHz int, ring *activity.Ring, idPrefix string) time.Duration {
	t.Helper()
	q := newQueue(t, newClock(), WithEarlyEviction(), WithObserver(obs))
	q.Register(&statelessAcceptListener{id: "h", typ: "T"})
	for i := 0; i < n; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("%s%d", idPrefix, i), Type: "T"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	var stop chan struct{}
	var readerDone chan struct{}
	if readerHz > 0 {
		stop = make(chan struct{})
		readerDone = make(chan struct{})
		go func() {
			defer close(readerDone)
			ticker := time.NewTicker(time.Second / time.Duration(readerHz))
			defer ticker.Stop()
			buf := make([]activity.Entry, 64)
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					ring.Read(0, buf)
				}
			}
		}()
	}

	start := time.Now()
	for i := 0; i < n; i++ {
		q.Dispatch()
	}
	elapsed := time.Since(start)

	if stop != nil {
		close(stop)
		<-readerDone
	}
	return elapsed
}

// minDispatchWallTime runs dispatchWallTime for `rounds` independent queues
// and returns the minimum — the standard technique for a stable wall-clock
// comparison on a shared/noisy machine (a scheduler hiccup or GC pause can
// only ever inflate one round's time, never deflate it, so the minimum
// across several rounds is the closest a black-box wall-clock measurement
// gets to "no interference").
func minDispatchWallTime(t *testing.T, n, rounds int, obs Observer, readerHz int, ring *activity.Ring, idPrefix string) time.Duration {
	t.Helper()
	var min time.Duration
	for r := 0; r < rounds; r++ {
		d := dispatchWallTime(t, n, obs, readerHz, ring, fmt.Sprintf("%s%d-", idPrefix, r))
		if r == 0 || d < min {
			min = d
		}
	}
	return min
}

// TestDispatchOverheadUnderRingReader is the Step 1 wall-time budget: wiring
// a Ring-backed Observer, including a concurrent 4Hz ring reader standing in
// for a live `status` poller, must add at most 5% wall-time overhead to
// Dispatch versus a no-op-observer baseline.
func TestDispatchOverheadUnderRingReader(t *testing.T) {
	const n = 6000
	const rounds = 6
	const maxOverheadPct = 5.0

	ring := activity.New(activity.DefaultSize)
	baseline := minDispatchWallTime(t, n, rounds, noopObserver{}, 0, nil, "b")

	cases := []struct {
		name     string
		readerHz int
	}{
		{"NoConcurrentReader", 0},
		{"With4HzConcurrentReader", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := minDispatchWallTime(t, n, rounds, &ringObserver{ring: ring}, tc.readerHz, ring, "o-"+tc.name)
			overheadPct := float64(got-baseline) / float64(baseline) * 100
			t.Logf("baseline=%v withRingObserver=%v overhead=%.2f%%", baseline, got, overheadPct)
			if overheadPct > maxOverheadPct {
				t.Fatalf("Dispatch wall-time overhead = %.2f%%, want <= %.1f%% (baseline=%v, observed=%v)",
					overheadPct, maxOverheadPct, baseline, got)
			}
		})
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
