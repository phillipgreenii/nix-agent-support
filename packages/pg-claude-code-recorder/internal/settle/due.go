package settle

import (
	"container/heap"
	"time"
)

// DueKind says what a deadline in the due-time heap is waiting for.
type DueKind int

const (
	// DueQuiet is a quiet-window or held-recheck deadline.
	DueQuiet DueKind = iota
	// DueRemoval is a removal deadline.
	DueRemoval
)

type dueKey struct {
	session string
	kind    DueKind
}

type dueEntry struct {
	key   dueKey
	at    time.Time
	index int
}

type dueQueue []*dueEntry

func (q dueQueue) Len() int { return len(q) }
func (q dueQueue) Less(i, j int) bool {
	if !q[i].at.Equal(q[j].at) {
		return q[i].at.Before(q[j].at)
	}
	if q[i].key.session != q[j].key.session {
		return q[i].key.session < q[j].key.session
	}
	return q[i].key.kind < q[j].key.kind
}

func (q dueQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *dueQueue) Push(x any) {
	e := x.(*dueEntry)
	e.index = len(*q)
	*q = append(*q, e)
}

func (q *dueQueue) Pop() any {
	old := *q
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return e
}

// DueHeap is a min-heap of (deadline, session) with at most one entry per
// (session, kind). It is a pure data structure: every time is injected and
// no wall clock is read. It is not safe for concurrent use.
type DueHeap struct {
	q     dueQueue
	index map[dueKey]*dueEntry
}

// NewDueHeap returns an empty heap.
func NewDueHeap() *DueHeap {
	return &DueHeap{index: map[dueKey]*dueEntry{}}
}

// Push schedules session at the given time as a DueQuiet entry, replacing
// any earlier DueQuiet entry for the same session.
func (h *DueHeap) Push(session string, at time.Time) {
	h.PushKind(session, DueQuiet, at)
}

// PushKind schedules session with an explicit kind, replacing any earlier
// entry for the same session and kind.
func (h *DueHeap) PushKind(session string, kind DueKind, at time.Time) {
	k := dueKey{session, kind}
	if e, ok := h.index[k]; ok {
		e.at = at
		heap.Fix(&h.q, e.index)
		return
	}
	e := &dueEntry{key: k, at: at}
	h.index[k] = e
	heap.Push(&h.q, e)
}

// Pop removes every entry whose deadline is at or before now and returns
// their sessions in deadline order, each session once even when both kinds
// are due.
func (h *DueHeap) Pop(now time.Time) []string {
	var out []string
	seen := map[string]bool{}
	for len(h.q) > 0 && !h.q[0].at.After(now) {
		e := heap.Pop(&h.q).(*dueEntry)
		delete(h.index, e.key)
		if !seen[e.key.session] {
			seen[e.key.session] = true
			out = append(out, e.key.session)
		}
	}
	return out
}

// Next returns the earliest deadline, or false when the heap is empty.
func (h *DueHeap) Next() (time.Time, bool) {
	if len(h.q) == 0 {
		return time.Time{}, false
	}
	return h.q[0].at, true
}

// Len reports the number of scheduled entries.
func (h *DueHeap) Len() int { return len(h.q) }

// DueItem is one popped entry.
type DueItem struct {
	Session string
	Kind    DueKind
}

// PopDue removes every entry whose deadline is at or before now and returns
// them in deadline order with their kinds (a session due under both kinds
// appears twice).
func (h *DueHeap) PopDue(now time.Time) []DueItem {
	var out []DueItem
	for len(h.q) > 0 && !h.q[0].at.After(now) {
		e := heap.Pop(&h.q).(*dueEntry)
		delete(h.index, e.key)
		out = append(out, DueItem{Session: e.key.session, Kind: e.key.kind})
	}
	return out
}
