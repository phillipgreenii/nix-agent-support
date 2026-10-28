package orchestrator

import (
	"sync"

	"github.com/phillipgreenii/pg-router/internal/transient"
)

// Transient handler-failure classes (bead pg2-yu5y2); the definitions and the
// classifier live in internal/transient so internal/metrics shares them.
const (
	RetryClassKilled      = transient.ClassKilled
	RetryClassDeadline    = transient.ClassDeadline
	RetryClassUnavailable = transient.ClassUnavailable
)

// DispatchRetryObserver is notified once per scheduled re-run of a failed
// dispatch (bead pg2-yu5y2); the production implementation is
// metrics.Emitter's pg_router_dispatch_retries_total{role,class}. nil
// disables the notification.
type DispatchRetryObserver interface {
	OnDispatchRetry(role, class string)
}

// classifyTransient reports whether err is a transient handler failure worth
// one more bounded attempt, and which class (see transient.Classify).
func classifyTransient(err error) (class string, ok bool) {
	return transient.Classify(err)
}

// retryLedger counts, per event id, the re-runs already scheduled for one
// listener. It is deliberately in-memory and transient: the queue persists no
// attempt history (DEC-EVENT-1), and a restart simply starts the count over,
// still bounded by the event's own expiry. Entries are removed the moment the
// event stops being retried (success, a non-transient failure, or exhaustion),
// so the map holds only events currently waiting on a retry.
type retryLedger struct {
	mu     sync.Mutex
	counts map[string]int
}

// take reserves one retry for eventID if fewer than max have been used,
// reporting whether it did. When it cannot, the entry is dropped.
func (r *retryLedger) take(eventID string, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts[eventID] >= max {
		delete(r.counts, eventID)
		return false
	}
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[eventID]++
	return true
}

func (r *retryLedger) forget(eventID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.counts, eventID)
}

func (r *retryLedger) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.counts)
}
