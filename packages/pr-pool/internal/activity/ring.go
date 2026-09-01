// Package activity provides a bounded, in-memory ring buffer of dispatch-
// outcome records (Task 3.4). It is a LEAF package: it imports nothing from
// any other Phase 3 task and, in particular, does not import
// internal/eventqueue — wiring a Ring as an eventqueue.Observer is
// cmd/pr-pool/run.go's job (a small adapter at the bootCore call site), not
// this package's own concern.
//
// A *Ring is meant to be read LIVE and directly by the status verb's handler
// (Task 3.8), not embedded in any periodic snapshot this task produces —
// Read copies the requested window into a caller-supplied buffer on demand.
package activity

import (
	"sync"
	"time"
)

// DefaultSize is the Ring's default capacity, used by New when size <= 0.
// internal/config's PR_POOL_ACTIVITY_RING overlays this at the operator
// boundary; config itself carries no literal copy of this constant (its
// ActivityRingSize default is the zero value, which New interprets the same
// way), so the one number lives here only.
const DefaultSize = 512

// defaultReadWindow is how many of the newest entries Read returns when
// since is the zero value — "omitted" and "explicitly 0" are, by design,
// the SAME request shape (Task 3.4 Binding decisions): a first request with
// no prior cursor wants a recent snapshot, not a full historical replay.
const defaultReadWindow = 64

// Entry is one recorded dispatch outcome.
type Entry struct {
	// Seq is assigned by Ring.Append itself: strictly increasing, starting at
	// 1, monotonic across any number of ring wraps. Any value the caller sets
	// on a Entry passed to Append is ignored/overwritten.
	Seq uint64
	// StartedAt is stamped onto every Entry by Append with the OWNING Ring's
	// own epoch (Ring.StartedAt) — a caller-supplied value is likewise
	// ignored/overwritten. A client compares this across two replies to
	// detect a stale cursor left over from a prior process incarnation; full
	// cross-restart cursor invalidation is that CLIENT's responsibility
	// (Task 4.0's TUI), not this package's — this field only makes the
	// signal available.
	StartedAt time.Time
	// Type is the event type this outcome is about (an eventqueue.Event.Type
	// at whatever call site produced it — this package has no opinion on the
	// value beyond "caller-supplied string").
	Type string
	// Outcome is an opaque verb sourced from report today, carrying no UI-
	// chrome vocabulary (ADR 0026): "delivered", "missed", "rejected",
	// "declined", "dispatch_failed", "deduped", "needs_input",
	// "budget_escalation". This package places no constraint on the value —
	// which of these a given production wiring can actually PRODUCE is that
	// wiring's own call-site decision (see cmd/pr-pool/run.go's
	// activityObserver for Task 3.4's own choice here).
	Outcome string
}

// Ring is a fixed-size, concurrency-safe ring buffer of Entry records.
// Append is O(1), non-blocking, and zero-allocation; Read copies into a
// caller-supplied buffer under the same mutex Append uses.
type Ring struct {
	mu        sync.Mutex
	buf       []Entry
	next      uint64 // next Seq to assign; nothing appended yet <=> next == 1
	startedAt time.Time
}

// New returns a Ring with the given capacity. size <= 0 falls back to
// DefaultSize — the one place both this package and internal/config agree
// on what "unconfigured" means, so the two cannot drift apart.
func New(size int) *Ring {
	if size <= 0 {
		size = DefaultSize
	}
	return &Ring{
		buf:       make([]Entry, size),
		next:      1,
		startedAt: time.Now(),
	}
}

// StartedAt returns this Ring's own epoch (its New call time). Stable for
// the Ring's whole lifetime — see Entry.StartedAt's doc for why a client
// cares.
func (r *Ring) StartedAt() time.Time {
	return r.startedAt
}

// Append records one outcome. O(1), non-blocking (a plain mutex, no I/O),
// never logs, and zero-allocation (TestAppendZeroAllocs) — Seq and
// StartedAt are stamped internally, overwriting whatever the caller passed
// in e.
func (r *Ring) Append(e Entry) {
	r.mu.Lock()
	e.Seq = r.next
	e.StartedAt = r.startedAt
	r.buf[e.Seq%uint64(len(r.buf))] = e
	r.next++
	r.mu.Unlock()
}

// Read copies entries newer than since into buf (oldest of the returned
// range first), returning how many were copied and whether some entries
// strictly between since and what is now retained were already overwritten
// before this call (dropped).
//
// The full since-cursor matrix (Task 3.4 Binding decisions):
//
//   - since == 0 (both an OMITTED request and an explicit zero value decode
//     to this — they are the same request shape): returns the newest
//     min(defaultReadWindow, held) entries; dropped is always false — there
//     is no cursor, so there is no gap to report against.
//   - since == newest: nothing new yet; n=0, dropped=false.
//   - since > newest: same as since == newest; n=0, dropped=false.
//   - since < oldest-retained: copies min(newest-since, capacity) entries
//     (i.e. every entry the ring still holds) and dropped=true, since some
//     entries in (since, oldest-retained) were already overwritten.
//   - otherwise (oldest-retained <= since < newest): copies exactly
//     newest-since entries (every entry with seq > since); dropped=false —
//     nothing in that range was lost.
//   - a negative or fractional since cannot reach this method at all — its
//     parameter is a uint64, so that malformed-value case is rejected at the
//     schema/wire layer (Task 3.8's schemas), not here.
//   - seq is strictly increasing in the returned window regardless of how
//     many times the ring has wrapped (Append's own invariant; see
//     TestAppend_seqStrictlyIncreasingAcrossMultipleWraps).
func (r *Ring) Read(since uint64, buf []Entry) (n int, dropped bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	newest := r.next - 1 // 0 if nothing has been appended yet
	capacity := uint64(len(r.buf))

	var want uint64
	if since == 0 {
		held := newest
		if held > capacity {
			held = capacity
		}
		want = defaultReadWindow
		if want > held {
			want = held
		}
	} else {
		if since >= newest {
			return 0, false
		}
		want = newest - since
		if want > capacity {
			want = capacity
			dropped = true
		}
	}

	start := newest - want + 1 // first Seq to return, ascending order
	for i := uint64(0); i < want && uint64(n) < uint64(len(buf)); i++ {
		seq := start + i
		buf[n] = r.buf[seq%capacity]
		n++
	}
	return n, dropped
}
