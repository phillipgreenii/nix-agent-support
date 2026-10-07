package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Transient handler-failure classes (bead pg2-yu5y2). They are the ONLY
// failures a role with MaxDispatchRetries > 0 re-runs; anything else (a
// deterministic failure such as a validation error or a bad exit code 1 with
// a reasoned message) is never retried, because running it again cannot help
// and would only add load. The class string is the `class` label of
// pg_router_dispatch_retries_total.
const (
	// RetryClassKilled: the handler subprocess was killed by a signal (os/exec
	// reports exit -1), or the handler reported that one of ITS children was
	// ("signal: killed" / "exit -1") — the shape a 30s connector timeout under
	// host overload produces.
	RetryClassKilled = "killed"
	// RetryClassDeadline: a deadline expired (context.DeadlineExceeded, or the
	// handler said so in its error text).
	RetryClassDeadline = "deadline"
	// RetryClassUnavailable: the handler or a backend it drives reported itself
	// unavailable (the connector wire taxonomy's retryable "unavailable" code).
	RetryClassUnavailable = "unavailable"
)

// DispatchRetryObserver is notified once per scheduled re-run of a failed
// dispatch (bead pg2-yu5y2); the production implementation is
// metrics.Emitter's pg_router_dispatch_retries_total{role,class}. nil
// disables the notification.
type DispatchRetryObserver interface {
	OnDispatchRetry(role, class string)
}

// classifyTransient reports whether err is a transient handler failure worth
// one more bounded attempt, and which class. A busy decline never reaches
// here (Offer maps it before), and a cancelled run context is excluded by the
// caller. Matching mirrors how the failure actually surfaces: the typed
// *wireclient.ExitError for a killed handler (-1), context.DeadlineExceeded
// structurally, and the handler's own error text for what it flattened into
// the reply (its child "signal: killed" / "exit -1:", "deadline exceeded",
// pg-desk's own `"error_class":"killed"|"deadline"` stderr line the command
// handler appends to its error, the connector's "unavailable" code).
func classifyTransient(err error) (class string, ok bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return "", false
	}
	var ee *wireclient.ExitError
	if errors.As(err, &ee) && ee.Code == -1 {
		return RetryClassKilled, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return RetryClassDeadline, true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "signal: killed"), strings.Contains(msg, "exit -1"),
		strings.Contains(msg, `"error_class":"killed"`):
		return RetryClassKilled, true
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, `"error_class":"deadline"`):
		return RetryClassDeadline, true
	case strings.Contains(msg, "unavailable"):
		return RetryClassUnavailable, true
	}
	return "", false
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
