package parser

import (
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// lagTestNow is the injected clock of lagTestState, for the seed.
var lagTestNow = time.Unix(0, 50_000_000_000)

func lagTestState() *lagState {
	return newLagState(func(s, _ string) bool { return s != "none" }, func(file int64) (string, string, bool) {
		if file >= 1000 {
			return "none", "append", true
		}
		return "src", "append", true
	}, func() time.Time { return lagTestNow })
}

func at(ns int64) time.Time { return time.Unix(0, ns) }

// secs returns the lag seconds at the injected time now.
func secs(l *lagState, now time.Time) float64 {
	_, s, _ := l.Gauges(now)
	return s
}

// Copy ahead of parse for N seconds with no parse progress reports N; after
// parse catches up it reports 0; a second copy while still behind does NOT
// reset the origin (a "latest copy time" design gets this wrong, so resetting
// on every copy fails here); a partial parse DOES move it to that time (parse
// lag is how long parse has been behind without advancing).
func TestLagSecondsRunFromWhenCopyFirstGotAheadOfParse(t *testing.T) {
	l := lagTestState()
	const s = int64(1_000_000_000)
	l.hook(1, 1, 10, 0, at(100*s)) // copy gets ahead at t=100
	if got := secs(l, at(107*s)); got != 7 {
		t.Fatalf("lag = %v s, want 7", got)
	}
	l.hook(1, 1, 20, 0, at(105*s)) // a second copy while still behind
	if got := secs(l, at(107*s)); got != 7 {
		t.Fatalf("after a second copy lag = %v s, want 7 (the origin must not reset)", got)
	}
	l.hook(1, 1, 20, 10, at(106*s)) // partial parse, still behind
	if got := secs(l, at(110*s)); got != 4 {
		t.Fatalf("after a partial parse lag = %v s, want 4 (the origin moves to the parse progress at t=106)", got)
	}
	l.hook(1, 1, 20, 20, at(111*s)) // parse catches up
	if got := secs(l, at(120*s)); got != 0 {
		t.Fatalf("caught up: lag = %v s, want 0", got)
	}
	l.hook(1, 1, 30, 20, at(130*s)) // copy gets ahead again: a new origin
	if got := secs(l, at(133*s)); got != 3 {
		t.Fatalf("a new lag episode = %v s, want 3", got)
	}
}

// The reported figure is the maximum over generations (the oldest origin), and
// a generation caught up drops out of it.
func TestLagSecondsIsTheOldestOriginAcrossGenerations(t *testing.T) {
	l := lagTestState()
	const s = int64(1_000_000_000)
	l.hook(1, 1, 10, 0, at(100*s))
	l.hook(2, 1, 5, 0, at(103*s))
	if got := secs(l, at(110*s)); got != 10 {
		t.Fatalf("oldest = %v s, want 10", got)
	}
	l.hook(1, 1, 10, 10, at(0))
	if got := secs(l, at(110*s)); got != 7 {
		t.Fatalf("after gen 1 caught up = %v s, want 7", got)
	}
	l.hook(2, 1, 5, 5, at(0))
	if b, sc, p := l.Gauges(at(110 * s)); b != 0 || sc != 0 || p != 1 {
		t.Fatalf("idle gauges = %d, %v, %v", b, sc, p)
	}
}

// A restart (a new lagState seeded from the tables) with parse behind copy
// reports from "now": every such generation starts at the injected time, and a
// caught-up one has no origin.
func TestLagSeedStartsBehindGenerationsAtNow(t *testing.T) {
	l := lagTestState()
	l.seed(
		[]store.File{{ID: 1, Source: "src"}, {ID: 2, Source: "src"}},
		[]store.Generation{{File: 1, Gen: 1, CopyOffset: 100, ParseOffset: 40}, {File: 2, Gen: 1, CopyOffset: 7, ParseOffset: 7}},
	)
	if got := secs(l, lagTestNow); got != 0 {
		t.Fatalf("lag right after the restart = %v s, want 0", got)
	}
	if got := secs(l, lagTestNow.Add(12*time.Second)); got != 12 {
		t.Fatalf("12 s after the restart lag = %v s, want 12", got)
	}
	gs := l.Generations()
	if !gs[1][0].UnparsedSince.Equal(lagTestNow) || !gs[2][0].UnparsedSince.IsZero() {
		t.Fatalf("origins = %v / %v", gs[1][0].UnparsedSince, gs[2][0].UnparsedSince)
	}
}

// The lag-seconds origin heap is lazy: a stale entry stays until it is on top
// and is valid only while its generation's origin still equals it.
func TestLagOldestOriginHeapIsLazilyPruned(t *testing.T) {
	l := lagTestState()
	const s = int64(1_000_000_000)
	now := at(10 * s)
	l.hook(1, 1, 10, 0, at(1*s))
	l.hook(2, 1, 5, 0, at(3*s))
	if got := secs(l, now); got != 9 {
		t.Fatalf("oldest = %v s, want 9", got)
	}
	l.hook(1, 1, 10, 10, at(0)) // gen 1 caught up: its entry is stale
	if got := secs(l, now); got != 7 {
		t.Fatalf("oldest = %v s, want 7", got)
	}
	l.hook(1, 1, 20, 10, at(5*s)) // gen 1 behind again with a newer origin
	if got := secs(l, now); got != 7 {
		t.Fatalf("oldest = %v s, want 7 (gen 2 still older)", got)
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

// Parse advances every few seconds on a hot file but never catches copy: the
// figure stays small and never reaches the 60 s the status verdict calls
// behind. Never refreshing the origin on parse progress (the old rule) makes
// the figure grow to the whole run and fails this test.
func TestLagSecondsStaySmallWhileParseAdvancesButNeverCatchesUp(t *testing.T) {
	l := lagTestState()
	const s = int64(1_000_000_000)
	const behindAfter = 60.0
	copyOff, parseOff := int64(1000), int64(0)
	l.hook(1, 1, copyOff, parseOff, at(0))
	for tick := int64(5); tick <= 600; tick += 5 { // ten minutes
		copyOff += 100
		l.hook(1, 1, copyOff, parseOff, at(tick*s-2*s)) // copy keeps growing
		parseOff += 50
		l.hook(1, 1, copyOff, parseOff, at(tick*s)) // parse advances, still behind
		if got := secs(l, at(tick*s+1*s)); got != 1 {
			t.Fatalf("t=%d: lag = %v s, want 1 (parse progressed 1 s ago)", tick, got)
		}
		if got := secs(l, at(tick*s+3*s)); got >= behindAfter {
			t.Fatalf("t=%d: lag = %v s reached the behind threshold while parse advances", tick, got)
		}
	}
	if b, _, _ := l.Gauges(at(601 * s)); b == 0 {
		t.Fatal("fixture error: parse caught up")
	}
}

// CurrentGenerations returns exactly the entry Generations holds for each
// file's named generation, and nothing for a file or generation not tracked.
func TestLagCurrentGenerationsMatchesGenerationsFilteredByCurrent(t *testing.T) {
	l := lagTestState()
	l.hook(1, 1, 10, 0, at(100))
	l.hook(1, 2, 30, 5, at(200))
	l.hook(2, 1, 7, 7, at(300))
	l.hook(3, 1, 9, 1, at(400))
	cur := map[int64]int{1: 2, 2: 1, 3: 4, 99: 1} // file 3's generation and file 99 are untracked
	got := l.CurrentGenerations(cur)
	want := map[int64]GenLag{}
	for f, gs := range l.Generations() {
		for _, g := range gs {
			if c, ok := cur[f]; ok && c == g.Gen {
				want[f] = g
			}
		}
	}
	if len(got) != 2 || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got[1].Gen != 2 || got[1].CopyOffset != 30 || got[1].ParseOffset != 5 {
		t.Fatalf("file 1 = %+v", got[1])
	}
	// The result is a private snapshot: later deliveries do not change it.
	l.hook(1, 2, 90, 90, at(500))
	if got[1].CopyOffset != 30 {
		t.Fatalf("snapshot changed after a delivery: %+v", got[1])
	}
	if l.CurrentGenerations(cur)[1].CopyOffset != 90 {
		t.Fatal("a fresh call must see the delivery")
	}
}

// Deliveries and CurrentGenerations calls interleave under -race.
func TestLagCurrentGenerationsRace(t *testing.T) {
	l := lagTestState()
	const files, steps = 8, 500
	cur := map[int64]int{}
	for f := int64(1); f <= files; f++ {
		cur[f] = 1
	}
	var wg sync.WaitGroup
	for f := int64(1); f <= files; f++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int64(1); i <= steps; i++ {
				l.hook(f, 1, i*10, i*5, at(i))
			}
		}()
	}
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-done:
				return
			default:
				for _, g := range l.CurrentGenerations(cur) {
					if g.ParseOffset > g.CopyOffset {
						t.Errorf("torn entry %+v", g)
						return
					}
				}
			}
		}
	}()
	wg.Wait()
	close(done)
	<-readerDone
	for f, g := range l.CurrentGenerations(cur) {
		if g.CopyOffset != steps*10 {
			t.Fatalf("file %d copy offset %d", f, g.CopyOffset)
		}
	}
}
