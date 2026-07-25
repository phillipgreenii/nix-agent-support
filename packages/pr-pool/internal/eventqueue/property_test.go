package eventqueue

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// trackingListener asserts the delivery invariants continuously as it receives
// offers: never an expired event, never the same id twice, and per-listener
// FIFO by enqueue index. binds nil means "binds everything"; a non-nil binds
// restricts it to those types (fan-out with distinct cursors). busy holds a
// per-id count of pre-accept declines to make before accepting (INV-FAIL-1 /
// INV-CONC-1); nil / 0 means accept on first offer.
type trackingListener struct {
	id       string
	binds    map[string]bool // nil => bind all
	busy     map[string]int  // per-id pre-accept declines remaining
	enqIndex map[string]int  // event id -> enqueue order index (shared, per-event)
	seen     map[string]bool
	lastIdx  int
	clk      *mockClock
	deadline map[string]time.Time
	t        *testing.T
}

func (l *trackingListener) ID() string { return l.id }
func (l *trackingListener) Matches(e Event) bool {
	if l.binds == nil {
		return true // binds everything
	}
	return l.binds[e.Type]
}

func (l *trackingListener) Offer(e Event) bool {
	// Invariant: never OFFERED an expired event (headFor filters expired).
	if !l.clk.now().Before(l.deadline[e.ID]) {
		l.t.Fatalf("delivered expired event %s at %v (deadline %v)", e.ID, l.clk.now(), l.deadline[e.ID])
	}
	// Pre-accept busy decline: the head is re-offered within ttl; do NOT consume
	// it (no FIFO advance, not marked seen).
	if n := l.busy[e.ID]; n > 0 {
		l.busy[e.ID] = n - 1
		return false
	}
	// Invariant: never the same id twice to this listener (within a retention
	// window; a fresh event reusing a retired id clears seen in the harness).
	if l.seen[e.ID] {
		l.t.Fatalf("duplicate delivery of %s to listener %s", e.ID, l.id)
	}
	// Invariant: per-listener FIFO — enqueue index is non-decreasing.
	idx := l.enqIndex[e.ID]
	if idx < l.lastIdx {
		l.t.Fatalf("out-of-order delivery: %s (idx %d) after idx %d", e.ID, idx, l.lastIdx)
	}
	l.lastIdx = idx
	l.seen[e.ID] = true
	return true
}

// checkSpineInvariant asserts the FIFO spine (q.order) is tombstone-free and
// duplicate-free — the structural invariant the evicted-id re-emit bug violated
// (bead pg2-f8btt) — and that DepthByType counts each retained non-expired id
// exactly once (INV-OBS-1). White-box: same package. Caller must NOT hold q.mu
// and must be single-threaded w.r.t. q (the property harness is).
func checkSpineInvariant(t *testing.T, q *Queue) {
	t.Helper()
	q.mu.Lock()
	now := q.now()
	seen := map[string]bool{}
	liveNonExpired := 0
	for _, id := range q.order {
		if seen[id] {
			q.mu.Unlock()
			t.Fatalf("FIFO spine carries id %q more than once (tombstone re-emit corruption, bead pg2-f8btt)", id)
		}
		seen[id] = true
		e, ok := q.entries[id]
		if !ok {
			q.mu.Unlock()
			t.Fatalf("FIFO spine carries id %q with no live entry (evict tombstone, bead pg2-f8btt)", id)
		}
		if now.Before(e.deadline()) {
			liveNonExpired++
		}
	}
	q.mu.Unlock()

	total := 0
	for _, n := range q.DepthByType() {
		total += n
	}
	if total != liveNonExpired {
		t.Fatalf("DepthByType total = %d, want %d (one per retained non-expired id; no double count)", total, liveNonExpired)
	}
}

// Randomized interleavings of enqueue / dispatch / expire / clock-advance, with
// duplicate ids and varied ttls, must never violate FIFO, dedup, or TTL
// (ADR 0031 property coverage). Deterministic: fixed seeds, mock clock.
func TestProperty_FIFODedupTTL(t *testing.T) {
	for seed := range int64(40) {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			clk := newClock()
			q := newQueue(t, clk)
			l := &trackingListener{
				id:       "h",
				enqIndex: map[string]int{},
				seen:     map[string]bool{},
				deadline: map[string]time.Time{},
				clk:      clk,
				t:        t,
			}
			q.Register(l)

			enqCount := 0
			for range 200 {
				switch rng.Intn(4) {
				case 0: // enqueue (sometimes a duplicate id)
					id := fmt.Sprintf("e%d", rng.Intn(30))
					ttl := time.Duration(1+rng.Intn(20)) * time.Minute
					// Record enqueue order + deadline only for a genuinely new/retained id.
					res, err := q.Enqueue(evt(id, "T", ttl))
					if err != nil {
						t.Fatal(err)
					}
					if res == Enqueued {
						l.enqIndex[id] = enqCount
						l.deadline[id] = clk.now().Add(ttl)
						delete(l.seen, id) // a fresh event with a reused id may be delivered again
						enqCount++
					}
				case 1:
					q.Dispatch()
				case 2:
					q.Expire()
				case 3:
					clk.advance(time.Duration(1+rng.Intn(5)) * time.Minute)
				}
			}
			// Drain: expire everything, dispatch what remains deliverable.
			clk.advance(time.Hour)
			q.Expire()
			q.Dispatch()
			if !q.Idle() {
				t.Fatalf("queue not idle after full drain")
			}
		})
	}
}

// With early eviction ON, plus fan-out (several listeners binding distinct type
// subsets) and pre-accept busy declines, the FIFO / dedup / TTL invariants MUST
// still hold AND the FIFO spine MUST stay tombstone-free and duplicate-free.
// Early eviction is exactly the path that previously left an evicted id in
// q.order, so a re-emit before the next Expire() double-counted it and jumped it
// ahead of earlier events (bead pg2-f8btt) — the coverage TestProperty_FIFODedupTTL
// lacked (no eviction, one always-accepting listener). Deterministic: fixed
// seeds, mock clock.
func TestProperty_EvictionFanoutBusy(t *testing.T) {
	types := []string{"T", "U"}
	for seed := range int64(60) {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			clk := newClock()
			q := newQueue(t, clk, WithEarlyEviction())

			// Per-event bookkeeping shared by all listeners (about the event, not
			// the listener); per-listener seen/lastIdx stay private.
			enqIndex := map[string]int{}
			deadline := map[string]time.Time{}

			nL := 2 + rng.Intn(2) // 2..3 listeners (fan-out)
			var listeners []*trackingListener
			for i := range nL {
				binds := map[string]bool{}
				for _, ty := range types {
					if rng.Intn(2) == 0 {
						binds[ty] = true
					}
				}
				if len(binds) == 0 { // ensure it binds at least one type
					binds[types[rng.Intn(len(types))]] = true
				}
				l := &trackingListener{
					id:       fmt.Sprintf("h%d", i),
					binds:    binds,
					busy:     map[string]int{},
					enqIndex: enqIndex,
					deadline: deadline,
					seen:     map[string]bool{},
					clk:      clk,
					t:        t,
				}
				listeners = append(listeners, l)
				q.Register(l)
			}

			enqCount := 0
			for range 300 {
				switch rng.Intn(5) {
				case 0, 1: // enqueue (often a duplicate / re-emit of a reused id)
					id := fmt.Sprintf("e%d", rng.Intn(15))
					ty := types[rng.Intn(len(types))]
					ttl := time.Duration(1+rng.Intn(20)) * time.Minute
					res, err := q.Enqueue(evt(id, ty, ttl))
					if err != nil {
						t.Fatal(err)
					}
					if res == Enqueued {
						enqIndex[id] = enqCount
						deadline[id] = clk.now().Add(ttl)
						enqCount++
						for _, l := range listeners {
							delete(l.seen, id) // fresh event with a reused id may redeliver
							if rng.Intn(3) == 0 {
								l.busy[id] = 1 + rng.Intn(2) // decline a few times first
							}
						}
					}
				case 2:
					q.Dispatch()
				case 3:
					q.Expire()
				case 4:
					clk.advance(time.Duration(1+rng.Intn(5)) * time.Minute)
				}
				// After every operation the spine must be tombstone- and
				// duplicate-free and DepthByType must not double-count.
				checkSpineInvariant(t, q)
			}
			// Drain: expire everything, then dispatch what remains deliverable.
			clk.advance(time.Hour)
			q.Expire()
			for range nL + 1 {
				q.Dispatch()
			}
			checkSpineInvariant(t, q)
			if !q.Idle() {
				t.Fatalf("queue not idle after full drain")
			}
		})
	}
}

// Native fuzz target over ParseTTL — exercises the wire ttl reader with
// arbitrary strings (never panics; positive-only acceptance).
func FuzzParseTTL(f *testing.F) {
	for _, s := range []string{"15m", "1h30m", "", "0s", "-1m", "abc", "9999h"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseTTL(s)
		if err == nil && d <= 0 {
			t.Fatalf("ParseTTL(%q) returned non-positive %v with no error", s, d)
		}
	})
}
