package activity

import (
	"sync"
	"testing"
	"time"
)

// Red-first (Task 3.4 Step 1): before this package existed this test failed
// to compile at all. It now asserts the zero-alloc contract Append promises.
func TestAppendZeroAllocs(t *testing.T) {
	r := New(8)
	e := Entry{Type: "t", Outcome: "delivered"}
	allocs := testing.AllocsPerRun(1000, func() {
		r.Append(e)
	})
	if allocs != 0 {
		t.Fatalf("Append allocs/op = %v, want 0", allocs)
	}
}

// --- the 7-case since-cursor matrix (Task 3.4 Binding decisions / Step 4) ---

// Case: since omitted (the zero value) returns the newest held entries when
// there are fewer than the read window.
func TestRead_sinceOmitted_returnsAllWhenFewerThanWindow(t *testing.T) {
	r := New(512)
	for i := 0; i < 10; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	buf := make([]Entry, 100)
	n, dropped := r.Read(0, buf)
	if n != 10 || dropped {
		t.Fatalf("n=%d dropped=%v, want 10,false", n, dropped)
	}
	if buf[0].Seq != 1 || buf[9].Seq != 10 {
		t.Fatalf("seqs = %d..%d, want 1..10", buf[0].Seq, buf[9].Seq)
	}
}

// Case: since omitted caps at the newest 64 entries even when many more are
// held.
func TestRead_sinceOmitted_capsAtDefaultWindow(t *testing.T) {
	r := New(512)
	for i := 0; i < 100; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	buf := make([]Entry, 200)
	n, dropped := r.Read(0, buf)
	if n != 64 || dropped {
		t.Fatalf("n=%d dropped=%v, want 64,false", n, dropped)
	}
	if buf[0].Seq != 37 || buf[63].Seq != 100 {
		t.Fatalf("seqs = %d..%d, want 37..100", buf[0].Seq, buf[63].Seq)
	}
}

// Case: since is the zero value, distinguished at the wire layer from
// "never set" (an omitted JSON field) but confirmed here to collapse onto
// exactly the same Ring behavior — there is only one Go-level request shape
// once it reaches Read(uint64, ...).
func TestRead_sinceZeroValue_collapsesWithOmitted(t *testing.T) {
	r := New(512)
	for i := 0; i < 5; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	var explicitZero uint64
	n1, d1 := r.Read(explicitZero, make([]Entry, 10))
	n2, d2 := r.Read(0, make([]Entry, 10))
	if n1 != n2 || d1 != d2 {
		t.Fatalf("since=explicit-zero (%d,%v) must collapse with since=omitted (%d,%v)", n1, d1, n2, d2)
	}
}

// Case: since == newest — nothing new has happened yet.
func TestRead_sinceEqualsNewest(t *testing.T) {
	r := New(512)
	for i := 0; i < 5; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	n, dropped := r.Read(5, make([]Entry, 10))
	if n != 0 || dropped {
		t.Fatalf("n=%d dropped=%v, want 0,false", n, dropped)
	}
}

// Case: since > newest — a cursor from the future (e.g. a client that
// mis-tracked its own watermark). Same "nothing new" answer as ==newest.
func TestRead_sinceGreaterThanNewest(t *testing.T) {
	r := New(512)
	for i := 0; i < 5; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	n, dropped := r.Read(999, make([]Entry, 10))
	if n != 0 || dropped {
		t.Fatalf("n=%d dropped=%v, want 0,false", n, dropped)
	}
}

// Case: since < oldest-retained — the requested range has already been
// partially overwritten by ring wraps, so dropped must be true and the
// result is bounded to what the ring still holds (capacity), not the full
// requested gap.
func TestRead_sinceBeforeOldestRetained_setsDropped(t *testing.T) {
	r := New(4) // tiny capacity to force wraps with few Appends
	for i := 0; i < 10; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"}) // seqs 1..10; ring now holds 7..10
	}
	buf := make([]Entry, 10)
	n, dropped := r.Read(1, buf) // gap = 10-1 = 9 > capacity(4)
	if !dropped {
		t.Fatal("dropped = false, want true (gap 9 > capacity 4)")
	}
	if n != 4 {
		t.Fatalf("n = %d, want 4 (bounded by capacity, not the requested gap)", n)
	}
	if buf[0].Seq != 7 || buf[3].Seq != 10 {
		t.Fatalf("seqs = %d..%d, want 7..10", buf[0].Seq, buf[3].Seq)
	}
}

// Boundary check alongside the dropped case above: since exactly at
// oldest-retained-1 asks for precisely what the ring still holds, so nothing
// was actually lost — dropped must be false at this exact boundary (gap ==
// capacity, not >).
func TestRead_sinceAtRetentionBoundary_notDropped(t *testing.T) {
	r := New(4)
	for i := 0; i < 10; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	buf := make([]Entry, 10)
	n, dropped := r.Read(6, buf) // gap = 10-6 = 4 == capacity
	if dropped {
		t.Fatal("dropped = true, want false (gap == capacity means nothing was actually lost)")
	}
	if n != 4 || buf[0].Seq != 7 || buf[3].Seq != 10 {
		t.Fatalf("n=%d seqs=%d..%d, want 4, 7..10", n, buf[0].Seq, buf[3].Seq)
	}
}

// Case: oldest-retained <= since < newest — an ordinary incremental poll;
// every requested entry is still held, so dropped is false.
func TestRead_sinceWithinRetainedWindow(t *testing.T) {
	r := New(512)
	for i := 0; i < 20; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}
	buf := make([]Entry, 20)
	n, dropped := r.Read(15, buf)
	if n != 5 || dropped {
		t.Fatalf("n=%d dropped=%v, want 5,false", n, dropped)
	}
	if buf[0].Seq != 16 || buf[4].Seq != 20 {
		t.Fatalf("seqs = %d..%d, want 16..20", buf[0].Seq, buf[4].Seq)
	}
}

// Case: a negative or fractional since. Ring.Read's signature takes a
// uint64, so this value cannot reach the method at all — Go's type system
// makes it structurally impossible to construct. That rejection is the
// schema/wire layer's job (Task 3.8's schemas), not this package's; this
// test exists only so the 7-case matrix's coverage of it is explicit and
// discoverable rather than silently absent.
func TestRead_malformedSince_isASchemaLayerConcern(t *testing.T) {
	t.Skip("negative/fractional since cannot reach a uint64 parameter; rejected at the schema layer (Task 3.8), not the Ring — see Task 3.4 Binding decisions")
}

// Case: seq stays strictly increasing across multiple full ring wraps
// (>1024 entries through a 512-capacity ring, i.e. two full wraps).
func TestAppend_seqStrictlyIncreasingAcrossMultipleWraps(t *testing.T) {
	const capacity = 512
	const total = capacity*2 + 37 // more than two full wraps
	r := New(capacity)
	for i := 0; i < total; i++ {
		r.Append(Entry{Type: "t", Outcome: "delivered"})
	}

	buf := make([]Entry, capacity)
	n, dropped := r.Read(uint64(total-capacity), buf) // exactly the retained window
	if n != capacity {
		t.Fatalf("n = %d, want %d", n, capacity)
	}
	if dropped {
		t.Fatal("dropped = true, want false (asked for exactly what's retained)")
	}
	for i := 1; i < n; i++ {
		if buf[i].Seq != buf[i-1].Seq+1 {
			t.Fatalf("seq not strictly increasing at index %d: %d -> %d", i, buf[i-1].Seq, buf[i].Seq)
		}
	}
	if buf[n-1].Seq != uint64(total) {
		t.Fatalf("last Seq = %d, want %d", buf[n-1].Seq, total)
	}
}

// --- epoch signal ---

// Epoch-mismatch: two Ring instances (simulating two process incarnations)
// must carry different StartedAt values, and every Entry a Ring produces
// must carry that SAME Ring's own StartedAt — the signal a client compares
// across two replies to detect a stale cursor from a prior incarnation.
func TestStartedAt_epochMismatchAcrossRingInstances(t *testing.T) {
	r1 := New(4)
	r1.Append(Entry{Type: "t", Outcome: "delivered"})
	time.Sleep(time.Millisecond) // guarantee a measurable clock difference
	r2 := New(4)
	r2.Append(Entry{Type: "t", Outcome: "delivered"})

	if r1.StartedAt().Equal(r2.StartedAt()) {
		t.Fatal("two Ring instances must have different StartedAt (the epoch-mismatch signal)")
	}

	buf1 := make([]Entry, 1)
	buf2 := make([]Entry, 1)
	r1.Read(0, buf1)
	r2.Read(0, buf2)
	if !buf1[0].StartedAt.Equal(r1.StartedAt()) {
		t.Fatal("an Entry read from r1 must carry r1's own StartedAt")
	}
	if !buf2[0].StartedAt.Equal(r2.StartedAt()) {
		t.Fatal("an Entry read from r2 must carry r2's own StartedAt")
	}
	if buf1[0].StartedAt.Equal(buf2[0].StartedAt) {
		t.Fatal("entries from two different Ring epochs must not share a StartedAt")
	}
}

// --- concurrency ---

// Multi-writer race: many goroutines (standing in for the production case of
// a drive loop and a socket-ingest observer both calling Append
// concurrently) must not lose or duplicate a seq assignment, and the result
// must stay strictly increasing. Sized so total <= the default read window
// and the ring's own capacity, so Read(0, ...) can verify the COMPLETE set
// via the public API alone. Run with -race (Task 3.4 Validation).
func TestAppend_multiWriterRace_noLostEntriesStrictlySeqIncreasing(t *testing.T) {
	const writers = 8
	const perWriter = 8
	const total = writers * perWriter // 64: fits Read(0, ...)'s default window exactly
	r := New(128)

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				r.Append(Entry{Type: "t", Outcome: "delivered"})
			}
		}()
	}
	wg.Wait()

	buf := make([]Entry, total)
	n, dropped := r.Read(0, buf)
	if n != total || dropped {
		t.Fatalf("n=%d dropped=%v, want %d,false", n, dropped, total)
	}
	seen := make(map[uint64]bool, total)
	for i, e := range buf[:n] {
		if seen[e.Seq] {
			t.Fatalf("duplicate Seq %d at index %d (a concurrent Append clobbered another's assignment)", e.Seq, i)
		}
		seen[e.Seq] = true
		if i > 0 && buf[i].Seq != buf[i-1].Seq+1 {
			t.Fatalf("seq not strictly increasing at index %d: %d -> %d", i, buf[i-1].Seq, buf[i].Seq)
		}
	}
	if len(seen) != total {
		t.Fatalf("saw %d distinct seqs, want %d (lost entries)", len(seen), total)
	}
}

// New(0) and a negative size both fall back to DefaultSize.
func TestNew_nonPositiveSizeFallsBackToDefault(t *testing.T) {
	for _, size := range []int{0, -1, -512} {
		r := New(size)
		if len(r.buf) != DefaultSize {
			t.Errorf("New(%d): capacity = %d, want DefaultSize %d", size, len(r.buf), DefaultSize)
		}
	}
}
