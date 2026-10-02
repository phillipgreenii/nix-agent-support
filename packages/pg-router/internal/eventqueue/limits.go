package eventqueue

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// This file ENFORCES the write-ahead log's size limit (bead pg2-5d3ui), on top of
// the compaction (compact.go) and the size gauge (LogSizer) bead pg2-8e0m6 landed.
//
// Two derived thresholds, one user-visible knob (max_log_bytes, the HARD limit):
//
//   - SOFT (90% of max by default): once a compaction attempt has not brought the
//     log back under it, the queue sets emittersHalted — an in-memory, queue-level
//     flag the producer (internal/discover) consults next to its gate check, so
//     polled command-source emitters are not polled. It is NOT a gate: the Gate
//     Registry, BlockingGate, CheckPull, dropGateBlockedLocked, NonBlockingGates and
//     run.go's gated drain-and-exit are untouched. Listeners keep dispatching and
//     run-until-idle keeps draining while halted, so space can be reclaimed and no
//     listener loses an event (INV-EVT-1 holds by construction); timer emitters and
//     pushed events keep being admitted — only the HARD limit stops those.
//
//   - HARD (max_log_bytes): Enqueue ADMISSION is refused with ErrLogFull. The limit
//     applies to enqueue admission and nothing else: accept, evict, gate_* and
//     compaction records are still appended above it (they use the soft-to-hard
//     headroom; refusing them would make an accept un-recordable, so the event would
//     be redelivered on every restart, and would stop the state change that frees
//     space from being recorded). A Deduped re-emit stays a no-write success.
//
//   - UNWRITABLE: the first append error (ENOSPC, EIO, ...) marks the log
//     unwritable. Admission is then refused with ErrLogUnwritable and the emitters
//     are halted. The state is volatile and in-memory — it never depends on a record
//     being writable — and is cleared by the first successful append or by the
//     periodic recovery probe (Prober, else a half-open retry) that
//     EnforceLogLimits runs each tick.
//
// The soft halt and the hard rejection are RE-DERIVED from the log size every tick
// (EnforceLogLimits / LimitStatus); nothing about them is persisted, so a restart
// re-evaluates them after startup compaction and a gate clear / resume --all cannot
// affect them.

// Reject reasons: the fixed, documented prefixes of a rejected event's reason
// text. Nothing machine-readable separates retry-later from permanent in the
// ingest reply (schemas/cli.ingest-event-reply.schema.json carries only {id,
// reason}), so the PREFIX is the contract.
const (
	ReasonLogFull       = "log_full"
	ReasonLogUnwritable = "log_unwritable"
)

// ErrLogFull is returned by Queue.Enqueue when admission is refused because the
// event log is at its hard limit. The event was NOT queued; retrying later is
// safe because delivery is idempotent (INV-EVT-2).
type ErrLogFull struct {
	Bytes, Limit int64
}

func (e *ErrLogFull) Error() string {
	return fmt.Sprintf("%s: pg-router event log is at %d of %d bytes; the event was NOT queued; safe to retry later. "+
		"To free space: wait for queued events to expire, run `pg-router log compact` (or restart pg-router; the log is compacted at startup), "+
		"or raise PG_ROUTER_MAX_LOG_BYTES and restart",
		ReasonLogFull, e.Bytes, e.Limit)
}

// ErrLogUnwritable is returned by Queue.Enqueue when admission is refused because
// the event log could not be written (disk full, I/O error, ...). The event was
// NOT queued; retrying later is safe because delivery is idempotent (INV-EVT-2).
type ErrLogUnwritable struct {
	// Err is the underlying write error that marked the log unwritable.
	Err error
}

func (e *ErrLogUnwritable) Error() string {
	cause := "write failed"
	if e.Err != nil {
		cause = e.Err.Error()
	}
	return fmt.Sprintf("%s: pg-router cannot write its event log (%s); the event was NOT queued; safe to retry later. "+
		"Check free disk space and permissions on the log directory; pg-router re-probes every tick and resumes by itself once it is writable",
		ReasonLogUnwritable, cause)
}

func (e *ErrLogUnwritable) Unwrap() error { return e.Err }

// RejectReason classifies an Enqueue error as one of the two admission
// refusals, returning its fixed reason prefix; ok is false for any other error.
func RejectReason(err error) (reason string, ok bool) {
	var full *ErrLogFull
	var unw *ErrLogUnwritable
	switch {
	case errors.As(err, &full):
		return ReasonLogFull, true
	case errors.As(err, &unw):
		return ReasonLogUnwritable, true
	}
	return "", false
}

// RejectObserver is an OPTIONAL extension of Observer (like RestoreObserver, and
// with precedent in core.IngestObserver.OnUnknownTypeRejected): an Observer that
// also implements it is told, once per event, about every Enqueue admission the
// queue refused, with the reason prefix (ReasonLogFull / ReasonLogUnwritable).
// It fires for EVERY producer (push ingest, pull sources, push-inject) because
// they share Queue.Enqueue — rejects are counted at that one choke point, not
// separately per path; discover's ProduceReport.LogRejected additionally breaks
// the pull side out per source. Like every observer hook it fires after q.mu is
// released.
type RejectObserver interface {
	OnEnqueueRejected(evtType, reason string)
}

// Prober is implemented by a Store that can cheaply check, without touching the
// log, whether it can be written again. FileStore writes and removes a small
// file beside the log. A Store that is not a Prober is recovered by a half-open
// retry instead: the next Enqueue is let through to try the real write.
type Prober interface {
	Probe() error
}

// Limit states, in precedence order (LimitStatus.State).
const (
	StateOK             = "ok"
	StateEmittersHalted = "emitters_halted"
	StateLogFull        = "log_full"
	StateLogUnwritable  = "log_unwritable"
)

// LimitStatus is a lock-free snapshot of the size-limit state, for status, the
// TUI and metrics.
type LimitStatus struct {
	// Bytes is the log's current size; SoftBytes / HardBytes are the derived
	// thresholds (0: that limit is not enforced).
	Bytes, SoftBytes, HardBytes int64
	// State is the single most severe state: log_unwritable, log_full,
	// emitters_halted, else ok.
	State string
	// EmittersHalted is true while polled command-source emitters are not
	// being polled (soft threshold crossed, or the log is unwritable).
	EmittersHalted bool
	// RejectedLogFull / RejectedLogUnwritable count the Enqueue admissions refused
	// since this process started, by reason.
	RejectedLogFull, RejectedLogUnwritable int64
	// Detail is the underlying write error while State is log_unwritable.
	Detail string
}

// Percent is Bytes as a percentage (0-100, may exceed 100) of HardBytes, or 0
// when no hard limit is set.
func (s LimitStatus) Percent() float64 {
	if s.HardBytes <= 0 {
		return 0
	}
	return float64(s.Bytes) * 100 / float64(s.HardBytes)
}

const (
	// probeInterval spaces recovery probes of an unwritable log after a failed
	// one; the first probe runs on the very next EnforceLogLimits call.
	probeInterval = 30 * time.Second
	// limitCompactRetry is how long a compaction that could not bring the log
	// under the soft threshold waits before another is attempted if the log has
	// not grown by limitCompactGrowth in the meantime (the thrash guard).
	limitCompactRetry  = 5 * time.Minute
	limitCompactGrowth = 1 << 20
)

// limitState is the queue's size-limit bookkeeping (limits.go).
type limitState struct {
	soft, hard int64 // thresholds, set once by WithLogLimits

	emittersHalted atomic.Bool
	unwritable     atomic.Bool
	detail         atomic.Pointer[string]
	nextProbe      atomic.Int64 // unix nanos of the earliest next probe; 0: now

	rejectedFull       atomic.Int64
	rejectedUnwritable atomic.Int64

	mu sync.Mutex // guards the compaction thrash-guard fields below
	// lastCompactAt / lastCompactSize record the latest limit-driven compaction
	// attempt: when it started, and the size the log was after it (or, until it
	// finishes, the size it started at).
	lastCompactAt   time.Time
	lastCompactSize int64
}

// WithLogLimits enables enforcement of the log-size limit: hardBytes is
// max_log_bytes (enqueue admission is refused at or above it), softBytes the
// derived threshold above which the emitters are halted. Either may be 0 to
// disable that limit. A Store that cannot report its size (LogSizer) is never
// size-limited, but the unwritable detection works regardless. See the file
// comment for the semantics.
func WithLogLimits(softBytes, hardBytes int64) Option {
	return func(q *Queue) {
		q.lim.soft = softBytes
		q.lim.hard = hardBytes
	}
}

// EmittersHalted reports whether polled command-source emitters should not be
// polled this tick: the log is over its soft threshold (after a compaction
// attempt) or unwritable. Lock-free. It is the in-memory flag discover consults
// next to its gate check; it is NOT a gate and is re-derived every tick by
// EnforceLogLimits.
func (q *Queue) EmittersHalted() bool { return q.lim.emittersHalted.Load() }

// LimitStatus snapshots the size-limit state. Lock-free; safe from status and
// metric callbacks.
func (q *Queue) LimitStatus() LimitStatus {
	size := q.LogSize()
	st := LimitStatus{
		Bytes:                 size,
		SoftBytes:             q.lim.soft,
		HardBytes:             q.lim.hard,
		EmittersHalted:        q.lim.emittersHalted.Load(),
		RejectedLogFull:       q.lim.rejectedFull.Load(),
		RejectedLogUnwritable: q.lim.rejectedUnwritable.Load(),
	}
	switch {
	case q.lim.unwritable.Load():
		st.State = StateLogUnwritable
		if d := q.lim.detail.Load(); d != nil {
			st.Detail = *d
		}
	case q.lim.hard > 0 && size >= q.lim.hard:
		st.State = StateLogFull
	case st.EmittersHalted:
		st.State = StateEmittersHalted
	default:
		st.State = StateOK
	}
	return st
}

// admitLocked is Enqueue's admission check, consulted only for an event that
// would be WRITTEN (a Deduped re-emit never reaches it). Caller holds q.mu.
func (q *Queue) admitLocked() error {
	if q.lim.unwritable.Load() {
		var cause error
		if d := q.lim.detail.Load(); d != nil {
			cause = errors.New(*d)
		}
		return &ErrLogUnwritable{Err: cause}
	}
	if q.lim.hard > 0 {
		if size := q.LogSize(); size >= q.lim.hard {
			return &ErrLogFull{Bytes: size, Limit: q.lim.hard}
		}
	}
	return nil
}

// noteWrite records the outcome of a store write for the unwritable detection and
// returns err unchanged. A failed write marks the log unwritable (detect on the
// FIRST append error) — except an EncodeError, which is a fault in the record,
// not in the storage. A successful write proves the log writable and clears the
// mark. Atomics only, so it is safe under q.mu.
func (q *Queue) noteWrite(err error) error {
	if err == nil {
		if q.lim.unwritable.Swap(false) {
			slog.Info("eventqueue: the event log is writable again; accepting events")
		}
		return nil
	}
	var enc *EncodeError
	if errors.As(err, &enc) {
		return err
	}
	msg := err.Error()
	q.lim.detail.Store(&msg)
	if !q.lim.unwritable.Swap(true) {
		q.lim.nextProbe.Store(0) // probe on the very next tick
		slog.Error("eventqueue: the event log could not be written; rejecting new events (log_unwritable) and halting polled emitters until a probe succeeds", "err", err)
	}
	return err
}

// appendLocked / appendBatchLocked are the queue's only paths to the Store's
// writes; each feeds noteWrite. Caller holds q.mu.
func (q *Queue) appendLocked(rec Record) error {
	return q.noteWrite(q.store.Append(rec))
}

func (q *Queue) appendBatchLocked(recs []Record) error {
	return q.noteWrite(q.store.AppendBatch(recs))
}

// countReject counts one refused admission (atomics only, so it is safe under
// q.mu) and returns the reason to hand notifyRejected AFTER q.mu is released
// ("" when err is not an admission refusal).
func (q *Queue) countReject(err error) string {
	reason, ok := RejectReason(err)
	if !ok {
		return ""
	}
	if reason == ReasonLogFull {
		q.lim.rejectedFull.Add(1)
	} else {
		q.lim.rejectedUnwritable.Add(1)
	}
	return reason
}

// notifyRejected fires the optional RejectObserver hook. Must be called with q.mu
// released.
func (q *Queue) notifyRejected(evtType, reason string) {
	if reason == "" {
		return
	}
	if ro, ok := q.obs.(RejectObserver); ok {
		ro.OnEnqueueRejected(evtType, reason)
	}
}

// EnforceLogLimits is the per-tick limit controller; the drive loop calls it
// once per tick BEFORE producing (so the emitters-halted decision it makes is the
// one that tick's producer sees) and independently of whether producing
// succeeds. It never blocks on the log:
//
//  1. If the log is unwritable, run the recovery probe (Prober, else a half-open
//     retry that lets the next Enqueue try the real write).
//  2. If the log is over its soft threshold, start a background compaction
//     (single-flight, guarded against thrash) — the compaction attempt that
//     precedes any halt.
//  3. Re-derive emittersHalted: over soft, or unwritable. Logged on transition.
//
// The log is compacted at startup (New) before the first call, so a log that is
// large only because of dead history never reaches the halt.
func (q *Queue) EnforceLogLimits() {
	now := q.now()
	if q.lim.unwritable.Load() {
		q.probe(now)
	}
	size := q.LogSize()
	over := q.lim.soft > 0 && size > q.lim.soft
	if over {
		q.compactForLimits(now, size)
	}
	halted := over || q.lim.unwritable.Load()
	if prev := q.lim.emittersHalted.Swap(halted); prev != halted {
		if halted {
			slog.Warn("eventqueue: halting polled emitters (listeners, timers and pushed events keep running)",
				"logBytes", size, "softBytes", q.lim.soft, "hardBytes", q.lim.hard, "unwritable", q.lim.unwritable.Load())
		} else {
			slog.Info("eventqueue: resuming polled emitters", "logBytes", size, "softBytes", q.lim.soft)
		}
	}
}

// probe attempts to clear the unwritable mark. A failed probe is retried no
// sooner than probeInterval later.
func (q *Queue) probe(now time.Time) {
	if next := q.lim.nextProbe.Load(); next != 0 && now.UnixNano() < next {
		return
	}
	pr, ok := q.store.(Prober)
	if !ok {
		// Half-open: let the next Enqueue attempt the real write. If it fails, the
		// failed append re-marks the log unwritable.
		if q.lim.unwritable.Swap(false) {
			slog.Info("eventqueue: retrying the unwritable event log on the next enqueue (store has no probe)")
		}
		return
	}
	if err := pr.Probe(); err != nil {
		q.lim.nextProbe.Store(now.Add(probeInterval).UnixNano())
		return
	}
	if q.lim.unwritable.Swap(false) {
		slog.Info("eventqueue: the event log is writable again; accepting events")
	}
}

// compactForLimits starts a background compaction when the log is over its soft
// threshold and a recent attempt has not already failed to get it under: another
// is made only once the log has grown by limitCompactGrowth since the last, or
// limitCompactRetry has passed (live state may have shrunk by expiry).
func (q *Queue) compactForLimits(now time.Time, size int64) {
	if _, ok := q.store.(Compactor); !ok {
		return
	}
	q.lim.mu.Lock()
	due := q.lim.lastCompactAt.IsZero() ||
		size >= q.lim.lastCompactSize+limitCompactGrowth ||
		now.Sub(q.lim.lastCompactAt) >= limitCompactRetry
	if !due || !q.compacting.CompareAndSwap(false, true) {
		q.lim.mu.Unlock()
		return
	}
	q.lim.lastCompactAt = now
	q.lim.lastCompactSize = size
	q.lim.mu.Unlock()
	q.compactWG.Add(1)
	go func() {
		defer q.compactWG.Done()
		defer q.compacting.Store(false)
		st, err := q.compact("limit")
		if err != nil {
			slog.Error("eventqueue: compaction at the soft log limit failed", "err", err)
			return
		}
		q.lim.mu.Lock()
		q.lim.lastCompactSize = st.BytesAfter
		q.lim.mu.Unlock()
	}()
}
