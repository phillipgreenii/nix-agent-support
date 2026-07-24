package eventqueue

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// trackingListener asserts the delivery invariants continuously as it receives
// offers: never an expired event, never the same id twice, and per-listener
// FIFO by enqueue index.
type trackingListener struct {
	id       string
	enqIndex map[string]int // event id -> enqueue order index
	seen     map[string]bool
	lastIdx  int
	clk      *mockClock
	deadline map[string]time.Time
	t        *testing.T
}

func (l *trackingListener) ID() string           { return l.id }
func (l *trackingListener) Matches(e Event) bool { return true } // binds everything
func (l *trackingListener) Offer(e Event) bool {
	// Invariant: never delivered an expired event.
	if !l.clk.now().Before(l.deadline[e.ID]) {
		l.t.Fatalf("delivered expired event %s at %v (deadline %v)", e.ID, l.clk.now(), l.deadline[e.ID])
	}
	// Invariant: never the same id twice to this listener.
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
