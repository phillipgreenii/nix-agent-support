package parser

import (
	"sync"
	"testing"
	"time"
)

func lagTestState() *lagState {
	return newLagState(func(s string) bool { return s != "none" }, func(file int64) (string, bool) {
		if file >= 1000 {
			return "none", true
		}
		return "src", true
	})
}

func at(ns int64) time.Time { return time.Unix(0, ns) }

// The oldest-unparsed heap is lazy: an entry stays until it is on top, and is
// valid only while its generation's front span still equals it.
func TestLagOldestUnparsedHeapIsLazilyPruned(t *testing.T) {
	l := lagTestState()
	now := at(10_000_000_000)
	l.hook(1, 1, 10, 0, at(1_000_000_000)) // gen 1: [0,10) seen 1s
	l.hook(1, 1, 20, 0, at(2_000_000_000)) // second span [10,20) seen 2s
	l.hook(2, 1, 5, 0, at(3_000_000_000))  // another file: [0,5) seen 3s
	if _, s, _ := l.Gauges(now); s != 9 {
		t.Fatalf("oldest = %v s, want 9 (seen 1s)", s)
	}
	// Parse passes the first span of gen 1: the origin moves to its second
	// commit (2s), the stale entry (1s) is pruned when it reaches the top.
	l.hook(1, 1, 20, 10, at(0))
	if _, s, _ := l.Gauges(now); s != 8 {
		t.Fatalf("after the partial parse oldest = %v s, want 8 (seen 2s)", s)
	}
	// Parse catches up on gen 1: only file 2's span remains.
	l.hook(1, 1, 20, 20, at(0))
	if _, s, _ := l.Gauges(now); s != 7 {
		t.Fatalf("oldest = %v s, want 7 (seen 3s)", s)
	}
	l.hook(2, 1, 5, 5, at(0))
	if b, s, p := l.Gauges(now); b != 0 || s != 0 || p != 1 {
		t.Fatalf("idle gauges = %d, %v, %v", b, s, p)
	}
}

// Spans are dropped as the parse offset passes them, so the list is bounded by
// the unparsed commits.
func TestLagSpanListIsBounded(t *testing.T) {
	l := lagTestState()
	for i := int64(1); i <= 1000; i++ {
		l.hook(1, 1, i*10, (i-1)*10, at(i))
	}
	if n := len(l.gens[lagKey{1, 1}].spans); n != 1 {
		t.Fatalf("%d spans kept, want 1", n)
	}
}

// A repeated or stale delivery changes nothing, and neither offset goes back.
func TestLagDeliveriesAreIdempotent(t *testing.T) {
	l := lagTestState()
	l.hook(1, 1, 30, 10, at(5))
	want := l.Generations()
	l.hook(1, 1, 30, 10, at(5))
	l.hook(1, 1, 20, 5, at(6)) // stale
	if got := l.Generations(); got[1][0] != want[1][0] {
		t.Fatalf("state moved: %v -> %v", want, got)
	}
}

// A source without a parser is tracked for the snapshot but out of every figure.
func TestLagSourceWithoutParserIsExcluded(t *testing.T) {
	l := lagTestState()
	l.hook(1000, 1, 500, 0, at(1))
	if b, s, p := l.Gauges(at(10_000_000_000)); b != 0 || s != 0 || p != 1 {
		t.Fatalf("an unparsed source leaked into the figures: %d, %v, %v", b, s, p)
	}
	if gs := l.FileGenerations(1000); len(gs) != 1 || gs[0].CopyOffset != 500 {
		t.Fatalf("snapshot = %+v", gs)
	}
}

// The heaps shed stale entries instead of growing with every change.
func TestLagHeapsCompact(t *testing.T) {
	l := lagTestState()
	for i := int64(1); i <= 2000; i++ {
		l.hook(1, 1, i, i-1, at(i))
	}
	if len(l.maxH) > 4*len(l.gens)+64 || len(l.oldH) > 4*len(l.gens)+64 {
		t.Fatalf("heaps grew: max %d, old %d", len(l.maxH), len(l.oldH))
	}
}

// Concurrent deliveries and reads under -race, then a deterministic end state.
func TestLagConcurrentOffsetUpdatesRace(t *testing.T) {
	l := lagTestState()
	const files, steps = 8, 500
	var wg sync.WaitGroup
	for f := int64(1); f <= files; f++ {
		wg.Add(2)
		go func() { // the copier side
			defer wg.Done()
			for i := int64(1); i <= steps; i++ {
				l.hook(f, 1, i*10, 0, at(i))
			}
		}()
		go func() { // the parser side
			defer wg.Done()
			for i := int64(1); i <= steps; i++ {
				l.hook(f, 1, i*10, i*10, at(0))
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				l.Gauges(at(1))
				l.Generations()
			}
		}
	}()
	wg.Wait()
	close(done)
	// Whatever the interleaving, both offsets reached their maximum, and the
	// parse side may have run ahead of a copy delivery, so settle with the
	// final deliveries in order.
	for f := int64(1); f <= files; f++ {
		l.hook(f, 1, steps*10, steps*10, at(0))
	}
	if b, s, p := l.Gauges(at(1)); b != 0 || s != 0 || p != 1 {
		t.Fatalf("end state = %d, %v, %v", b, s, p)
	}
}
