package eventqueue

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// trackingListener asserts the delivery invariants continuously as it receives
// offers:
//
//   - INV-EVT-4: at most ONE attempt is made past the event's `expiresAt`, and
//     nothing is ever offered again after that final attempt. (An expired event IS
//     offered — expiry is checked AT ATTEMPT TIME and decides whether the attempt
//     is the last one, it does not suppress the attempt. That is the reversal of
//     what a duration-bounded queue would assert here.)
//   - never the same id ACCEPTED twice;
//   - per-listener FIFO by enqueue index.
//
// binds nil means "binds everything"; a non-nil binds restricts it to those types
// (fan-out with distinct cursors). busy holds a per-id count of pre-accept
// declines to make before accepting (INV-FAIL-1 / INV-CONC-1); nil / 0 means
// accept on first offer. Note that a busy count may never be worked off: if the
// event expires first, the attempt made past `expiresAt` is the last one owed and
// the listener simply never gets to accept — correct behavior, not a failure.
type trackingListener struct {
	id       string
	binds    map[string]bool // nil => bind all
	busy     map[string]int  // per-id pre-accept declines remaining
	enqIndex map[string]int  // event id -> enqueue order index (shared, per-event)
	seen     map[string]bool // ids this listener has accepted
	final    map[string]bool // ids whose final (post-expiry) attempt is already made
	lastIdx  int
	clk      *mockClock
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
	// INV-EVT-4: the final attempt is FINAL. Once an attempt has been made on an
	// expired event, that (event, listener) pair is settled and the core must never
	// offer it again — a fresh event reusing the id clears this in the harness.
	if l.final[e.ID] {
		l.t.Fatalf("event %s re-offered to %s after its final (post-expiry) attempt (INV-EVT-4)", e.ID, l.id)
	}
	// The queue stores the RESOLVED event, so the offered copy carries the
	// authoritative expiry: the listener can make the same comparison the core did.
	if e.Expired(l.clk.now()) {
		l.final[e.ID] = true
	}
	// Pre-accept busy decline: an UNEXPIRED head is re-offered (INV-FAIL-1); do NOT
	// consume it (no FIFO advance, not marked seen).
	if n := l.busy[e.ID]; n > 0 {
		l.busy[e.ID] = n - 1
		return false
	}
	// Invariant: never accept the same id twice (within one retention window; a
	// fresh event reusing a retired id clears seen in the harness).
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
// (bead pg2-f8btt) — and that DepthByType counts each RETAINED id exactly once
// (INV-OBS-1). Depth counts retained events, expired or not, because under
// INV-EVT-4 an expired event is still held until its owed attempts are made.
// White-box: same package. Caller must NOT hold q.mu and must be single-threaded
// w.r.t. q (the property harness is).
func checkSpineInvariant(t *testing.T, q *Queue) {
	t.Helper()
	q.mu.Lock()
	seen := map[string]bool{}
	for _, id := range q.order {
		if seen[id] {
			q.mu.Unlock()
			t.Fatalf("FIFO spine carries id %q more than once (tombstone re-emit corruption, bead pg2-f8btt)", id)
		}
		seen[id] = true
		if _, ok := q.entries[id]; !ok {
			q.mu.Unlock()
			t.Fatalf("FIFO spine carries id %q with no live entry (evict tombstone, bead pg2-f8btt)", id)
		}
	}
	retained := len(q.order)
	q.mu.Unlock()

	total := 0
	for _, n := range q.DepthByType() {
		total += n
	}
	if total != retained {
		t.Fatalf("DepthByType total = %d, want %d (one per retained id; no double count)", total, retained)
	}
}

// drain runs dispatch/expire passes until the queue is idle and empty. One head
// per listener per pass, so a full drain needs as many passes as there are
// retained events; the bound makes a stuck queue fail instead of hang.
func drain(t *testing.T, q *Queue, passes int) {
	t.Helper()
	for range passes {
		q.Dispatch()
		q.Expire()
		if q.Idle() && len(q.DepthByType()) == 0 {
			return
		}
	}
	t.Fatalf("queue not drained after %d passes: idle=%v depth=%v", passes, q.Idle(), q.DepthByType())
}

// Randomized interleavings of enqueue / dispatch / expire / clock-advance, with
// duplicate ids and a mix of BORN-EXPIRED defaults and explicit expiry windows,
// must never violate FIFO, dedup, or the INV-EVT-4 final-attempt rule (ADR 0031
// property coverage). Deterministic: fixed seeds, mock clock.
func TestProperty_FIFODedupExpiry(t *testing.T) {
	for seed := range int64(40) {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			clk := newClock()
			q := newQueue(t, clk)
			l := &trackingListener{
				id:       "h",
				enqIndex: map[string]int{},
				seen:     map[string]bool{},
				final:    map[string]bool{},
				clk:      clk,
				t:        t,
			}
			q.Register(l)

			enqCount := 0
			for range 200 {
				switch rng.Intn(4) {
				case 0: // enqueue (sometimes a duplicate id)
					id := fmt.Sprintf("e%d", rng.Intn(30))
					e := evt(id, "T")
					// One in four is the DEFAULT (born expired); the rest ask for a
					// window, so both the one-shot and the retry paths are exercised.
					if rng.Intn(4) != 0 {
						e = evtUntil(id, "T", clk.in(time.Duration(1+rng.Intn(20))*time.Minute))
					}
					res, err := q.Enqueue(e)
					if err != nil {
						t.Fatal(err)
					}
					if res == Enqueued {
						l.enqIndex[id] = enqCount
						// A fresh event reusing a retired id may be delivered again, and
						// is owed its own final attempt.
						delete(l.seen, id)
						delete(l.final, id)
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
			clk.advance(time.Hour)
			drain(t, q, 60)
		})
	}
}

// With early eviction ON, plus fan-out (several listeners binding distinct type
// subsets) and pre-accept busy declines, the FIFO / dedup / final-attempt
// invariants MUST still hold AND the FIFO spine MUST stay tombstone-free and
// duplicate-free. Early eviction is exactly the path that previously left an
// evicted id in q.order, so a re-emit before the next Expire() double-counted it
// and jumped it ahead of earlier events (bead pg2-f8btt) — the coverage
// TestProperty_FIFODedupExpiry lacks (no eviction, one always-accepting listener).
// Deterministic: fixed seeds, mock clock.
func TestProperty_EvictionFanoutBusy(t *testing.T) {
	types := []string{"T", "U"}
	for seed := range int64(60) {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			clk := newClock()
			q := newQueue(t, clk, WithEarlyEviction())

			// Per-event bookkeeping shared by all listeners (about the event, not
			// the listener); per-listener seen/final/lastIdx stay private.
			enqIndex := map[string]int{}

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
					seen:     map[string]bool{},
					final:    map[string]bool{},
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
					e := evt(id, ty)
					if rng.Intn(4) != 0 {
						e = evtUntil(id, ty, clk.in(time.Duration(1+rng.Intn(20))*time.Minute))
					}
					res, err := q.Enqueue(e)
					if err != nil {
						t.Fatal(err)
					}
					if res == Enqueued {
						enqIndex[id] = enqCount
						enqCount++
						for _, l := range listeners {
							delete(l.seen, id) // fresh event with a reused id may redeliver
							delete(l.final, id)
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
			clk.advance(time.Hour)
			drain(t, q, 60)
			checkSpineInvariant(t, q)
		})
	}
}

// Native fuzz target over the wire INSTANT reader — the reader that replaced the
// duration parser when expiry became an absolute instant (DEC-EVENT-1). The
// property is encode/decode SYMMETRY: every instant the reader accepts must
// survive formatInstant → parseInstant unchanged, because that pair is what a
// push-inject front door uses to forward an already-decoded event to a core in
// another process. An instant that shifted in transit would change the receiving
// core's INV-EVT-4 verdict for that event.
func FuzzParseInstant(f *testing.F) {
	for _, s := range []string{
		"",
		"2026-07-16T12:00:00Z",
		"2026-07-16T12:00:00.123456789Z",
		"2026-07-16T12:00:00+02:00",
		"2026-07-16T12:00:00-05:30",
		"yesterday",
		"15m",
		"2026-07-16 12:00:00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := parseInstant("expiresAt", s)
		if err != nil {
			return // rejected, and never a panic: that is the other half of the property
		}
		if got.IsZero() {
			return // absent (or genuinely the zero instant): nothing to round-trip
		}
		again, err := parseInstant("expiresAt", formatInstant(got))
		if err != nil {
			t.Fatalf("parseInstant(%q) -> %s, which formatInstant rendered unreadably: %v", s, got, err)
		}
		if !again.Equal(got) {
			t.Fatalf("instant shifted through a format/parse round trip: %q -> %s -> %s", s, got, again)
		}
	})
}
