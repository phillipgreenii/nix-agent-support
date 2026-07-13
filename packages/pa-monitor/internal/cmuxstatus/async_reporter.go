package cmuxstatus

import "sync"

// asyncReporter decouples sidebar painting from the caller's goroutine. It wraps
// a synchronous Reporter and runs its Push on a dedicated worker, so a slow (or
// hung) `cmux` CLI can never block the caller.
//
// The bridge's gRPC receive loop is that caller: before this decorator, a
// blocked `cmux set-status` inside Push stalled the loop past its 4s snapshot
// watchdog and produced a spurious "Lost connection to daemon". With the paint
// off the loop, the daemon stream stays serviced no matter how slow cmux is.
//
// Scheduling is latest-wins: Push enqueues onto a depth-1 buffer, discarding any
// not-yet-started snapshot first, so a burst collapses to the newest state and a
// slow painter never falls behind a backlog. A single producer is assumed (the
// bridge pushes from one goroutine); nothing else refills the buffer.
type asyncReporter struct {
	inner   Reporter
	pending chan Snapshot
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newAsyncReporter(inner Reporter) *asyncReporter {
	a := &asyncReporter{
		inner:   inner,
		pending: make(chan Snapshot, 1),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go a.run()
	return a
}

// run drains the latest queued snapshot and paints it, until Clear stops it.
// A paint in flight when done closes completes before run returns, so Clear's
// join guarantees no paint overlaps the inner Clear.
func (a *asyncReporter) run() {
	defer close(a.stopped)
	for {
		select {
		case <-a.done:
			return
		case s := <-a.pending:
			a.inner.Push(s)
		}
	}
}

// Push replaces any queued-but-unstarted snapshot with s and returns
// immediately; it never blocks on the paint. Safe for a single producer.
func (a *asyncReporter) Push(s Snapshot) {
	select {
	case <-a.done:
		return
	default:
	}
	// Coalesce: drop a stale queued snapshot, then enqueue the fresh one. The
	// second send cannot block for a single producer — we just emptied the
	// buffer and nothing else fills it.
	select {
	case <-a.pending:
	default:
	}
	select {
	case a.pending <- s:
	case <-a.done:
	}
}

// Notify delegates synchronously. It has no production caller on a hot path and
// touches none of the inner's single-writer state, so it need not be queued.
func (a *asyncReporter) Notify(title, body string) {
	a.inner.Notify(title, body)
}

// Clear stops the worker, joins it (so any in-flight paint has completed), then
// runs the inner Clear. Idempotent: repeat calls are no-ops. The join is bounded
// by the inner paint's own timeout.
func (a *asyncReporter) Clear() {
	a.once.Do(func() {
		close(a.done)
		<-a.stopped
		a.inner.Clear()
	})
}
