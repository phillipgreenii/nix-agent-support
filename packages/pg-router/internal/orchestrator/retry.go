package orchestrator

import (
	"log/slog"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
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

// DispatchFailedObserver is notified once per dispatch that ENDED failed: a
// handler error that is not handed back for a dispatch re-run (a deterministic
// failure, or a transient one whose retries are spent or whose window is closed).
// A transient attempt that is re-run reaches DispatchRetryObserver instead, so the
// two are mutually exclusive per attempt, unlike HandlerFailureObserver, which
// sees every failed attempt. The production implementation is metrics.Emitter's
// pg_router_dispatch_failed_total{type,role}. nil disables the notification.
type DispatchFailedObserver interface {
	OnDispatchFailed(evtType, listenerID string)
}

// DispatchLevelObserver measures work against the resource levels it started
// at. DispatchLevels snapshots the graded-scale resources as {resource, level}
// pairs (two-level and unknown-level resources omitted); Offer takes it before
// the handler runs. OnDispatchAtLevel is told, once per dispatch that ended
// (accepted and finished, not skipped, not handed back for a retry, a busy
// decline or a shutdown), the role, that snapshot, the handler's run time and
// whether the dispatch ended failed, the same notion as DispatchFailedObserver.
// The production implementation is metrics.Emitter's
// pg_router_dispatch_at_level_total and pg_router_dispatch_at_level_seconds_total.
// nil disables both calls.
type DispatchLevelObserver interface {
	DispatchLevels() [][2]string
	OnDispatchAtLevel(role string, at [][2]string, ran time.Duration, failed bool)
}

// classifyTransient reports whether err is a transient handler failure worth
// one more bounded attempt, and which class (see transient.Classify).
func classifyTransient(err error) (class string, ok bool) {
	return transient.Classify(err)
}

// RetryLog is the durable home of the retry count (ADR 0121): the event queue
// persists one record per scheduled re-run and rebuilds the count on replay, so
// a restart no longer starts it over. *eventqueue.Queue implements it. nil keeps
// the ledger purely in memory.
type RetryLog interface {
	// RecordRetry durably records that listenerID has scheduled count re-runs of
	// eventID.
	RecordRetry(eventID, listenerID string, count int) error
	// RetryCount reports the durable count for (eventID, listenerID), 0 if none.
	RetryCount(eventID, listenerID string) int
}

// retryLedger counts, per event id, the re-runs already scheduled for one
// listener. The in-memory map is a cache over the durable count in log (ADR
// 0121, DEC-EVENT-1): a count is appended to the queue log only when a re-run is
// scheduled (never for a decline, a busy offer or a first attempt), read back
// from it when the map has no entry (after a restart), and dropped by the queue
// with the event (eviction, compaction) or on accept. Map entries are removed the
// moment the event stops being retried (success, a non-transient failure, or
// exhaustion), so the map holds only events currently waiting on a retry.
type retryLedger struct {
	mu     sync.Mutex
	counts map[string]int
	// log and listenerID name the durable store and this ledger's listener; log
	// nil means in-memory only.
	log        RetryLog
	listenerID string
}

// count returns the re-runs used for eventID, seeding from the durable log when
// the map has no entry. Caller holds r.mu.
func (r *retryLedger) count(eventID string) int {
	if n, ok := r.counts[eventID]; ok {
		return n
	}
	if r.log == nil {
		return 0
	}
	return r.log.RetryCount(eventID, r.listenerID)
}

// take reserves one retry for eventID if fewer than max have been used,
// reporting whether it did. When it cannot, the entry is dropped.
func (r *retryLedger) take(eventID string, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.count(eventID)
	if n >= max {
		delete(r.counts, eventID)
		return false
	}
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[eventID] = n + 1
	if r.log != nil {
		if err := r.log.RecordRetry(eventID, r.listenerID, n+1); err != nil {
			slog.Warn("orchestrator: retry count not persisted; a restart will not remember it",
				"listener", r.listenerID, "event", eventID, "err", err)
		}
	}
	return true
}

// used reports how many re-runs have been reserved for eventID so far.
func (r *retryLedger) used(eventID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count(eventID)
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

// FailedLog is the durable home of the dead-letter record: when a dispatch ends
// terminally failed, the listener appends one failed record to the event queue's
// log. *eventqueue.Queue implements it. nil records nothing.
type FailedLog interface {
	RecordFailed(dl eventqueue.DeadLetter) error
}

// DeadLetterFor builds the failed record for a dispatch of evt to role that ended
// failed with err at instant at, attempt being the 1-based attempt that ended it.
// The class is the router's transient class, or deterministic for any other
// failure; the reason is deterministic for a deterministic class, else
// window-closed when the event had expired at the failure instant (the queue's own
// Expired test, so equality counts), else retries-spent.
func DeadLetterFor(evt eventqueue.Event, role string, err error, attempt int, at time.Time) eventqueue.DeadLetter {
	class, transientFailure := classifyTransient(err)
	reason := eventqueue.DeadReasonDeterministic
	if !transientFailure {
		class = eventqueue.ClassDeterministic
	} else if !evt.ExpiresAt.IsZero() && evt.Expired(at) {
		reason = eventqueue.DeadReasonWindowClosed
	} else {
		reason = eventqueue.DeadReasonRetriesSpent
	}
	return eventqueue.DeadLetter{EventID: evt.ID, Role: role, Class: class, Attempts: attempt, Reason: reason, At: at.UTC().Truncate(time.Second)}
}
