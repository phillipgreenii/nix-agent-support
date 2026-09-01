package eventqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
)

// Listener is a bound event handler as the queue sees it (INTF-HANDLER, core
// side). It declares which events it binds (Matches, INV-DISP-1) and accepts or
// declines an offer (Offer). Offer takes an Offering — this attempt's dispatch
// tracking id (Task 2.2, dsp-<12 hex>, minted before q.mu is taken for the
// pass) plus the event — and returns an OfferResult. An OfferResult with
// Accepted=false is a PRE-ACCEPT decline (busy / unavailable, INV-CONC-1 /
// INV-FAIL-1, classified by DeclineReason): the core re-offers the event while
// it is unexpired, at the cadence INV-FAIL-2 defines (WithRetryBackoff, or a
// per-listener override via BackoffListener). Once Offer returns
// Accepted=true the event is ACCEPTED and the core's delivery responsibility
// ends (INV-EVT-1); post-accept retry/resume is the handler's. "Accept is not
// settle": ACCEPTANCE is this method returning true; SETTLEMENT is Dispatch's
// phase-3 bookkeeping for the (event, listener) pair — a distinction the
// deferred form (Phase 5) widens further, once acceptance and completion can
// happen in different calls entirely.
type Listener interface {
	ID() string
	Matches(evt Event) bool
	Offer(o Offering) OfferResult
}

// Offering is one dispatch attempt handed to Listener.Offer: the tracking id
// minted for this attempt (Task 2.2) plus the event being offered. ID fills
// deliveries[].id (Task 0.4) once dispatch surfaces through status (Task 3.0).
type Offering struct {
	ID    string
	Event Event
}

// OfferResult is what Listener.Offer reports back for one Offering: whether
// the offer was accepted, and if not, why (DeclineReason).
type OfferResult struct {
	Accepted bool
	Decline  DeclineReason
}

// DeclineReason classifies a pre-accept decline (an OfferResult with
// Accepted=false). The queue's re-offer/backoff behavior (INV-FAIL-1 /
// INV-FAIL-2) is IDENTICAL for every reason — DeclineNone re-offers exactly
// like DeclineBusy; the classification exists purely for observability
// (Task 2.3's Observer widening), never for queue control flow.
type DeclineReason int

const (
	// DeclineNone is the catch-all: a decline that is neither a graceful busy
	// signal nor an unavailable participant. Every in-tree Listener
	// implementation Task 2.2 lands always accepts (none has a reason to
	// decline yet), so this is also what an implementation with no more
	// specific reason to offer would report; Task 2.3 wires the first
	// genuine non-None reasons.
	DeclineNone DeclineReason = iota
	// DeclineBusy is a graceful "not right now" decline (INV-CONC-1): the
	// handler could take this event, just not yet.
	DeclineBusy
	// DeclineUnavailable is a decline because the handler itself is not
	// currently reachable/ready (its registered lifecycle state, Task 2.1).
	DeclineUnavailable
)

// String renders d for the Observer's OnDeclined `reason` argument (Task
// 2.3): observability text only, never consulted by the queue's own control
// flow (every DeclineReason re-offers identically, per this type's own doc).
func (d DeclineReason) String() string {
	switch d {
	case DeclineBusy:
		return "busy"
	case DeclineUnavailable:
		return "unavailable"
	default:
		return "none"
	}
}

// BackoffListener is a Listener that declares its OWN handler retry cadence
// (INV-FAIL-2), overriding the queue's default (WithRetryBackoff) for just this
// handler. Handlers differ in how long they typically stay busy, so the cadence
// is per-handler, not pool-wide only — a Listener that does not implement this
// simply uses the queue's default.
type BackoffListener interface {
	Listener
	RetryBackoff() backoff.Policy
}

// Observer receives queue lifecycle signals for metric emission (INV-OBS-1).
// The OTel emitter (bead pg2-hvlyj.18) implements it; the default is a no-op.
type Observer interface {
	OnEnqueue(evt Event)
	OnAccept(eventID, listenerID string)
	OnUnconsumedExpired(evtType string)
	// OnDeclined fires on a PRE-ACCEPT decline — Listener.Offer returning false
	// without panicking (INV-FAIL-1): a graceful `busy` or `unavailable` reply.
	// It fires on EVERY declined offer for evtType, not only the terminal
	// (INV-EVT-4 last-attempt) one, so a chronically busy handler's backlog is
	// never under-counted until its final, expiry-bound attempt.
	//
	// (operator scope-cut 2026-07-28: everything post-accept stays permanently
	// out of scope.)
	//
	// Task 2.3 widens the signature with listenerID (which handler declined)
	// and reason (DeclineReason.String(), observability text only — the
	// queue's own re-offer/backoff treatment stays IDENTICAL for every
	// DeclineReason, per that type's doc). This is purely additive data for a
	// consumer (Task 3.0's activity ring/metrics) — it does not change WHEN
	// or how often OnDeclined fires.
	OnDeclined(evtType, listenerID, reason string)
	// OnDispatchFailure fires on the OTHER delivery-side failure class
	// (INV-OBS-1): an outright dispatch failure where the core's own attempt to
	// hand the event to a listener broke outright rather than returning a
	// graceful accept/decline reply — currently, a recovered panic from
	// Listener.Offer (see offerSafely). It is distinct from OnDeclined: a
	// single Offer()==false used to be the only signal at this boundary,
	// covering both a graceful busy decline and an outright dispatch failure
	// with no way to tell them apart; this hook is what makes them separable
	// (bead pg2-icm3u). Like OnDeclined it fires on EVERY occurrence and
	// follows the identical settlement/retry mechanics (INV-FAIL-1 / INV-EVT-4)
	// — only the failure-rate metric class differs.
	OnDispatchFailure(evtType string)
	// OnDeduped fires when Enqueue drops a re-emit of a still-RETAINED id
	// (INV-EVT-3, the Deduped EnqueueResult) — a signal that, before Task
	// 2.3, had no observer hook at all (the review digest's corr-5 gap:
	// "Nonexistent data: ... `deduped` hook").
	OnDeduped(evtType string)
}

type noopObserver struct{}

func (noopObserver) OnEnqueue(Event)                   {}
func (noopObserver) OnAccept(string, string)           {}
func (noopObserver) OnUnconsumedExpired(string)        {}
func (noopObserver) OnDeclined(string, string, string) {}
func (noopObserver) OnDeduped(string)                  {}
func (noopObserver) OnDispatchFailure(string)          {}

// EnqueueResult reports whether an enqueue added a new event or was dropped as
// a duplicate of a still-retained id (INV-EVT-3).
type EnqueueResult int

const (
	// Enqueued means the event was newly appended to the durable queue.
	Enqueued EnqueueResult = iota
	// Deduped means an event with the same id is still RETAINED, so this re-emit
	// was dropped (INV-EVT-3). Because the retained-id set lives exactly as long
	// as the event does, the de-dup window narrows with `expiresAt`: under the
	// born-expired default it is roughly one dispatch cycle, so a pull source's
	// next-trigger re-emit is NOT absorbed — "re-emission, not resurrection"
	// (DEC-EVENT-1).
	Deduped
)

type entry struct {
	evt Event // RESOLVED (Event.Resolve): both instants are concrete
	// accepted tracks ACCEPTANCE per (event, listener) (INV-EVT-1) and is the
	// durable half of the pair (opAccept records).
	accepted map[string]bool
	// settled tracks which listeners are owed NO FURTHER ATTEMPT for this event:
	// they accepted it, or they made an attempt while it was already expired,
	// which INV-EVT-4 makes the last one. It is a TERMINAL MARKER, not an attempt
	// log — there is no count here and none is persisted (DEC-EVENT-1): a decline
	// before `expiresAt` records nothing at all, so it is simply re-offered
	// (INV-FAIL-1). settled is a superset of accepted.
	settled map[string]bool
}

func newEntry(evt Event) *entry {
	return &entry{evt: evt, accepted: map[string]bool{}, settled: map[string]bool{}}
}

type listenerState struct {
	l Listener

	// Handler retry cadence bookkeeping (INV-FAIL-2). declineEventID/declineStreak
	// track CONSECUTIVE pre-accept declines against the CURRENT head, so a streak
	// resets when the head moves on (accepted, or settled by expiry) or when a
	// decline lands against a DIFFERENT event id than the one currently tracked.
	// nextEligible is the earliest instant the core will offer this listener its
	// head again. All three are TRANSIENT (never persisted): a restart simply
	// re-offers immediately, which is always a legal (if perhaps premature) retry
	// — the cadence is a courtesy to a busy handler, not a correctness guarantee.
	declineEventID string
	declineStreak  int
	nextEligible   time.Time

	// delivered/declined are Task 2.3's per-listener delivery counters (Step
	// 2.3.6), incremented inside Dispatch's phase 3 ONLY — under the
	// ALREADY-HELD q.mu, never from an observer hook (those fire unlocked,
	// Step 2.3.5) and never behind a separate mutex. Plain ints are safe
	// here precisely because every access is under q.mu; Queue's own
	// pool-wide delivered/declined below are atomic because THOSE are read
	// without q.mu (a future status surface, Task 3.0).
	delivered int
	declined  int
}

// eligibleNow reports whether ls's retry-cadence cool-down (if any) for head
// event id has elapsed. It is deliberately SEPARATE from headFor: headFor picks
// WHICH event is owed an attempt (structural, expiry-blind, INV-EVT-4); this
// decides WHEN the core may next spend an attempt on it (cadence, INV-FAIL-2).
// Idle() and DepthByType() must keep using headFor alone — a cooling-down head
// is still owed work, so the queue must not read as idle nor its depth as
// drained merely because the cadence is withholding the next attempt.
func (ls *listenerState) eligibleNow(id string, now time.Time) bool {
	return ls.declineEventID != id || !now.Before(ls.nextEligible)
}

// recordDecline advances ls's consecutive-decline streak against event id and
// sets the next instant the core may offer this listener its head again
// (INV-FAIL-2). A decline against a DIFFERENT event id than the one currently
// tracked starts a fresh streak — the cadence backs off per (listener, head),
// not across unrelated heads a listener happens to decline over its lifetime.
func (ls *listenerState) recordDecline(id string, now time.Time, p backoff.Policy) {
	if ls.declineEventID != id {
		ls.declineEventID = id
		ls.declineStreak = 0
	}
	ls.declineStreak++
	ls.nextEligible = now.Add(p.Duration(ls.declineStreak))
}

// resetBackoff clears ls's decline streak: the listener accepted its head, or
// its last owed attempt just settled, so there is nothing left to back off
// from.
func (ls *listenerState) resetBackoff() {
	ls.declineEventID = ""
	ls.declineStreak = 0
	ls.nextEligible = time.Time{}
}

// depthCell is the immutable snapshot published under q.mu after every
// mutation to q.entries/q.order (Task 3.2's four mutation sites: Enqueue's
// add, retireLocked, maybeEvict, replay's opEnqueue/opEvict handling).
// DepthByType and UnmatchedBindings read it lock-free (cell.Load()) instead of
// scanning q.entries under q.mu. Never mutated in place once stored — a
// mutation site builds a NEW depthCell (copy-on-write) and Stores it, so a
// concurrent lock-free reader always sees either the pre- or the fully
// post-mutation state, never a half-applied one.
type depthCell struct {
	depth    map[string]int      // per-type retained count
	everSeen map[string]struct{} // per-type "enqueued at least once this run" (add-only)
}

// Queue is the durable, ordered, de-duped, retention-bounded event queue
// (ADR 0031, expiry bound amended by DEC-EVENT-1). It is safe for concurrent use.
type Queue struct {
	// mu guards every mutable field below.
	//
	// LOCK-ORDER INVARIANT (Task 2.3, pg2-84o3m.22; review-digest perf-F11):
	// q.mu is a LEAF w.r.t. internal/core's Registry.mu and the (future,
	// Task 3.0) activity ring — no code path may acquire q.mu while holding
	// either of those, in either order. This is why the registry-aware
	// unavailable check lives in internal/orchestrator's Offer, consulted in
	// Dispatch's UNLOCKED phase 2, and never in Matches/ID (which run under
	// q.mu via headFor): a check reachable from headFor would need to take
	// Registry.mu WHILE q.mu is held, an AB/BA deadlock risk the moment
	// anything else ever takes q.mu while holding Registry.mu. It is also
	// why Task 2.3's observer hooks (Step 2.3.5) fire strictly AFTER q.mu is
	// released — a ring with its own mutex, or an OTel gauge callback that
	// reads back into the queue (DepthByType), must never be called while
	// q.mu is held, for the identical reason.
	mu   sync.Mutex
	cell atomic.Pointer[depthCell]
	now  func() time.Time
	// after is the wait seam RunUntilIdle blocks on between passes (default
	// time.After). It is paired with the `now` clock seam so a mock clock can
	// drive BOTH coherently: a mock `after` advances virtual time by the tick and
	// fires immediately, so RunUntilIdle terminates on expiry deterministically
	// without real sleeping (see WithSleeper). The real default genuinely waits.
	after func(time.Duration) <-chan time.Time
	store Store
	obs   Observer

	// retryBackoff is the DEFAULT handler retry cadence (INV-FAIL-2) — how long
	// the core waits before re-offering a listener its head after a pre-accept
	// decline, before expiresAt bounds it (INV-EVT-4). A BackoffListener overrides
	// this per-handler; every other listener uses this default (backoff.Default()
	// unless overridden by WithRetryBackoff).
	retryBackoff backoff.Policy

	entries map[string]*entry // by event id; the retained set (and so the dedup id set)
	order   []string          // enqueue order (event ids) — the FIFO spine

	listeners []*listenerState // registration order; stable per-listener cursors
	// listenerCount mirrors len(listeners) and is updated (under q.mu) every
	// time Register appends. Dispatch reads it WITHOUT q.mu, atomically, to
	// size the batch of dispatch ids it mints before taking the phase-1 lock
	// (Task 2.2) — len(listeners) itself cannot be read safely outside q.mu
	// (the slice header mutates on append), so this lock-free mirror is what
	// makes "minted before q.mu is taken" possible without guessing.
	listenerCount atomic.Int64

	// evictWhenAllAccept is the opt-in early-eviction switch (ADR 0031). Default
	// off: keep every event until its retention ends. On: evict once all
	// currently-bound listeners have accepted (disk savings; shortens the dedup
	// window — safe only when the consumer set is fixed).
	evictWhenAllAccept bool

	// serializeTypes is the set of event TYPES marked to serialize (INV-CONC-1,
	// mechanism resolved by
	// `phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions ·
	// DEC-CONC-1`, closing the former OQ-CONC-MARK): headFor never returns more
	// than one entry of a marked type at a time as ANY listener's head — across
	// every bound listener, not merely the same one — until that entry is
	// RELEASED (releasedLocked). nil/empty (the default) leaves every type's
	// dispatch completely unaffected.
	serializeTypes map[string]bool

	// custody holds exactly the offers OUTSTANDING in phase 2 of the CURRENT
	// dispatch pass (Task 2.2): recorded (under q.mu) the moment each offer's
	// dispatch id is assigned in phase 1, deleted (under q.mu) in phase 3 once
	// that offer's outcome is known — WHATEVER that outcome is (accept, a
	// final decline that settles the pair, a decline that simply re-offers
	// next pass, or the underlying entry vanishing mid-offer and never
	// settling at all — retireLocked's return covers that last case reaching
	// phase 3 the same way). "Accept is not settle": a custody entry's
	// removal in phase 3 means only that THIS PASS's offer has concluded,
	// never that the (event, listener) pair itself is done — a re-offer next
	// pass mints an entirely fresh dispatch id and a fresh custody entry.
	// SessionsInFlight reads this map LIVE under q.mu at call time; it is
	// NEVER cached in any periodic snapshot. Before Phase 5's deferred-settle
	// form, len(custody) is always 0 or 1, since Dispatch's phase 2 offers one
	// listener at a time, synchronously, within a single goroutine.
	custody map[string]custody

	// delivered/declined are Task 2.3's POOL-WIDE delivery counters (Step
	// 2.3.6) — the aggregate counterpart to each listenerState's own
	// per-listener tally above. Written from Dispatch's phase 3, which
	// already holds q.mu (same discipline as listenerCount's write side);
	// atomic.Int64 because they are meant to be READ WITHOUT q.mu (a future
	// status surface, Task 3.0) — the same read-without-the-lock rationale
	// listenerCount already documents.
	delivered atomic.Int64
	declined  atomic.Int64
}

// custody is one outstanding-offer record in Queue.custody. It carries no
// payload today — Task 2.2 needs only presence/cardinality (SessionsInFlight,
// the custody-pin test) — but is a NAMED type per the design ("a custody
// map[string]custody"), not a bare map[string]struct{}, so a later task can
// widen it (e.g. the listener/event a given dispatch id belongs to) without
// changing Queue.custody's declared shape. See that field's doc for the full
// lifecycle.
type custody struct{}

// Option configures a Queue.
type Option func(*Queue)

// WithClock injects a clock seam (default time.Now) for deterministic expiry
// tests.
func WithClock(now func() time.Time) Option { return func(q *Queue) { q.now = now } }

// WithSleeper injects the wait seam RunUntilIdle blocks on between passes
// (default time.After). It is the companion to WithClock: a mock clock supplies
// an `after` that ADVANCES its virtual time by the requested tick and fires
// immediately, so RunUntilIdle drains and terminates on expiry deterministically
// without sleeping real time (and without the frozen-clock busy-loop the real
// time.After caused under a mock clock).
func WithSleeper(after func(time.Duration) <-chan time.Time) Option {
	return func(q *Queue) { q.after = after }
}

// WithObserver installs a metrics Observer (INV-OBS-1).
func WithObserver(o Observer) Option { return func(q *Queue) { q.obs = o } }

// WithEarlyEviction opts into evicting an event once all bound listeners have
// accepted it (ADR 0031 "opt-in early eviction").
func WithEarlyEviction() Option { return func(q *Queue) { q.evictWhenAllAccept = true } }

// WithSerializeTypes marks each named event TYPE to serialize (INV-CONC-1,
// `phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions ·
// DEC-CONC-1`): the queue offers at most one entry of a marked type at a
// time — across EVERY bound listener, not merely per-listener, which the
// existing per-listener FIFO already guarantees on its own — until that entry
// is RELEASED: settled (accepted, or given its one attempt past `expiresAt`,
// INV-EVT-4) for every CURRENTLY-BOUND listener that matches it. Release is
// deliberately NOT eviction/retirement — see releasedLocked and headFor for
// why. A type never named here dispatches exactly as before; this option is
// additive and opt-in, per type. Passing the same name more than once (in one
// call or across calls) is harmless.
func WithSerializeTypes(types ...string) Option {
	return func(q *Queue) {
		if q.serializeTypes == nil {
			q.serializeTypes = map[string]bool{}
		}
		for _, t := range types {
			q.serializeTypes[t] = true
		}
	}
}

// WithRetryBackoff overrides the queue's DEFAULT handler retry cadence
// (INV-FAIL-2): how long the core waits before re-offering a listener its head
// after a pre-accept decline. A Listener MAY override this per-handler by
// implementing BackoffListener; this Option sets what every OTHER listener
// uses. Default: backoff.Default().
func WithRetryBackoff(p backoff.Policy) Option {
	return func(q *Queue) { q.retryBackoff = p }
}

// retryBackoffFor returns the policy that governs l's re-offer cadence: l's own
// override when it implements BackoffListener, else the queue's default.
func (q *Queue) retryBackoffFor(l Listener) backoff.Policy {
	if bl, ok := l.(BackoffListener); ok {
		return bl.RetryBackoff()
	}
	return q.retryBackoff
}

// unlockOnce returns a function that unlocks q.mu exactly once, no matter how
// many times it is called (Task 2.3, Step 2.3.5's panic-safe-unlock
// pattern). A caller that needs to fire an observer hook AFTER releasing
// q.mu — never while it is held, per the lock-order invariant on q.mu's own
// doc — calls the returned function explicitly at that point, while ALSO
// deferring it as usual: a panic anywhere between Lock and the explicit call
// (e.g. a panicking Store double) still releases the lock exactly once,
// because the explicit call and the deferred fallback share the same
// sync.Once. A raw q.mu.Unlock() call is forbidden in any function using
// this pattern — it would defeat exactly the panic safety this exists for.
func (q *Queue) unlockOnce() func() {
	var once sync.Once
	return func() { once.Do(q.mu.Unlock) }
}

// New constructs a Queue over store, replaying any prior durable state so
// delivery survives a restart (INV-EVT-1). Evicted ids are not resurrected.
func New(store Store, opts ...Option) (*Queue, error) {
	q := &Queue{
		now:          time.Now,
		after:        time.After,
		store:        store,
		obs:          noopObserver{},
		retryBackoff: backoff.Default(),
		entries:      map[string]*entry{},
		custody:      map[string]custody{},
	}
	q.cell.Store(&depthCell{depth: map[string]int{}, everSeen: map[string]struct{}{}})
	for _, opt := range opts {
		opt(q)
	}
	if err := q.replay(); err != nil {
		return nil, err
	}
	return q, nil
}

// replay reconstructs in-memory state from the durable log. Accept records write
// after acceptance is confirmed (ADR 0031 req 4), so an event whose accept
// record was lost to a crash window replays as un-accepted and is re-offered
// (at-least-once, at-most-one redelivery).
//
// Every enqueued-and-not-evicted event is restored, INCLUDING one already past
// its `expiresAt`. Skipping those would break the delivery-opportunity guarantee
// outright (INV-EVT-1, "nothing is ever dropped un-offered"): under the
// born-expired default EVERY event is past expiry the instant it lands, so a
// past-expiry filter here would mean the durable queue survived no restart at
// all. The log is authoritative instead — a retired event carries an explicit
// opEvict record (see retireLocked), so replay drops exactly what actually left
// the queue and nothing else.
func (q *Queue) replay() error {
	recs, err := q.store.Replay()
	if err != nil {
		return err
	}
	for _, r := range recs {
		switch r.Op {
		case opEnqueue:
			e := newEntry(r.event())
			if _, seen := q.entries[e.evt.ID]; !seen {
				q.order = append(q.order, e.evt.ID)
				// A genuinely new retained entry (the `!seen` branch, mirrored from
				// everSeen's own doc): count it and mark its type ever-enqueued.
				q.publishCellLocked("", e.evt.Type, e.evt.Type)
			}
			q.entries[e.evt.ID] = e
		case opAccept:
			if e, ok := q.entries[r.EventID]; ok {
				e.accepted[r.ListenerID] = true
				e.settled[r.ListenerID] = true
			}
		case opEvict:
			if e, ok := q.entries[r.EventID]; ok {
				q.publishCellLocked(e.evt.Type, "", "")
			}
			delete(q.entries, r.EventID)
			q.dropFromOrder(r.EventID) // no tombstone: a re-emit must re-append fresh
		}
	}
	return nil
}

// Register binds a listener to the queue. Its per-listener cursor is
// independent of every other listener's (ADR 0031); fan-out delivers a matching
// event to each bound listener.
func (q *Queue) Register(l Listener) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.listeners = append(q.listeners, &listenerState{l: l})
	q.listenerCount.Store(int64(len(q.listeners)))
}

// Enqueue durably appends an event (INV-EVT-1). A malformed event is rejected
// (Validate). Its optional instants are RESOLVED here, against the core's own
// clock, because ingest is where INV-EVT-1 says the defaults come from. A re-emit
// of an id still RETAINED is dropped as a duplicate (INV-EVT-3). The enqueue
// record is persisted BEFORE the in-memory add, so an accepted-then-crashed event
// is never lost. A re-emit of an id whose retention is OVER (the stale-retire
// branch below) batches the old entry's evict record with the new enqueue record
// into one AppendBatch call, still persisted before either entry's in-memory
// mutation: a batch failure returns the error with NEITHER the stale entry
// retired NOR the re-emit admitted, exactly as a plain single-append failure
// already left the re-emit un-admitted.
//
// Task 2.3 (Step 2.3.5): every observer hook below fires AFTER q.mu is
// released, via the panic-safe unlockOnce helper — never synchronously while
// locked (the lock-order invariant on q.mu's own doc). Because the
// stale-retire branch's evict+enqueue is now ONE atomic AppendBatch call
// (above), a batch failure leaves NOTHING changed — neither the stale entry
// retired nor the re-emit admitted — so, unlike an unbatched retire, that
// failure return owes no hook at all; only the Deduped return and the two
// success returns do.
func (q *Queue) Enqueue(evt Event) (EnqueueResult, error) {
	if err := evt.Validate(); err != nil {
		return Enqueued, err
	}
	q.mu.Lock()
	unlock := q.unlockOnce()
	defer unlock()
	now := q.now()
	evt = evt.Resolve(now)
	enqueueRecord := recordFromEvent(evt, now)
	if e, ok := q.entries[evt.ID]; ok {
		if q.retainedLocked(e, now) {
			unlock()
			q.obs.OnDeduped(evt.Type)
			return Deduped, nil // still-retained duplicate id (INV-EVT-3)
		}
		// The id exists but its retention is over and the sweep has not run yet. A
		// re-emit is a FRESH event and MUST go to the tail (FIFO), not reuse the
		// stale position — so retire the stale entry first, on exactly the terms
		// Expire would have retired it on (same miss accounting, same durable
		// record), so which of the two removes it cannot change what is observed.
		// The evict half is built (recordEvictLocked) but not yet applied to
		// q.entries/q.order — both mutations wait until the batch below succeeds.
		evictRecord := q.recordEvictLocked(e.evt.ID)
		if err := q.store.AppendBatch([]Record{evictRecord, enqueueRecord}); err != nil {
			return Enqueued, err
		}
		staleMiss := len(e.accepted) == 0
		delete(q.entries, e.evt.ID)
		q.publishCellLocked(e.evt.Type, evt.Type, evt.Type)
		q.dropFromOrder(evt.ID)
		q.entries[evt.ID] = newEntry(evt)
		q.order = append(q.order, evt.ID)
		unlock()
		if staleMiss {
			q.obs.OnUnconsumedExpired(e.evt.Type)
		}
		q.obs.OnEnqueue(evt)
		return Enqueued, nil
	}
	if err := q.store.Append(enqueueRecord); err != nil {
		return Enqueued, err
	}
	q.entries[evt.ID] = newEntry(evt)
	q.order = append(q.order, evt.ID)
	q.publishCellLocked("", evt.Type, evt.Type)
	unlock()
	q.obs.OnEnqueue(evt)
	return Enqueued, nil
}

// dropFromOrder removes every occurrence of id from the FIFO spine so the spine
// never carries a stale duplicate position for a re-enqueued id. Caller holds
// q.mu.
func (q *Queue) dropFromOrder(id string) {
	kept := q.order[:0:0]
	for _, x := range q.order {
		if x != id {
			kept = append(kept, x)
		}
	}
	q.order = kept
}

// retainedLocked reports whether an entry must stay in the queue. Retention ends
// only when BOTH halves of INV-EVT-1's retention rule are satisfied — the event
// is past `expiresAt` (so its id no longer bounds a de-dup window, INV-EVT-3) AND
// no currently-bound matching listener is still owed the one attempt INV-EVT-1
// guarantees it.
//
// The second half is what keeps the born-expired default honest. Such an event is
// past expiry the moment it lands, so expiry alone would drop it before anything
// was offered it; holding it for the outstanding attempt is what makes
// "unconsumed-expired" a GENUINE miss rather than a scheduling artifact
// (INV-DISP-3). An event no bound listener matches satisfies the second half
// vacuously and is dropped at expiry, which is INV-DISP-3's SECOND case — a type
// a configured binding declares whose binding is inactive this run, expected and
// neither an error nor a warning. It is NOT the no-binding case: an event whose
// type no configured binding declares at all is rejected at ingest and never
// reaches this queue (internal/core's handleIngestEvent).
//
// Caller holds q.mu.
func (q *Queue) retainedLocked(e *entry, now time.Time) bool {
	if !e.evt.Expired(now) {
		return true
	}
	for _, ls := range q.listeners {
		if ls.l.Matches(e.evt) && !e.settled[ls.l.ID()] {
			return true // a bound listener is still owed its final attempt
		}
	}
	return false
}

// releasedLocked reports whether e — an event of a SERIALIZE-marked type
// (INV-CONC-1) — has been released by every currently-bound listener: each
// listener that Matches it is settled (accepted, or given its one attempt
// after `expiresAt`, INV-EVT-4). Once released, e no longer occupies its
// type's serialize slot (headFor), even though it may still be RETAINED in
// the queue (retainedLocked) — release is a DELIVERY question ("has every
// bound handler had its one attempt"), retention is a DEDUP-WINDOW question
// ("how long does the id stay live"), and the two genuinely diverge exactly
// when an event is fully settled before its own `expiresAt`: absent
// WithEarlyEviction, ADR 0031 keeps it retained until `expiresAt` regardless,
// but INV-CONC-1's release condition is worded "completes **or expires**" —
// completing means HANDLED, not evicted. Gating release on
// eviction/retirement instead would let a promptly-and-fully-accepted
// serialized event go on blocking its successor for the rest of its retention
// window even though every bound handler already took it, which is the
// reading INV-CONC-1 documents as rejected.
//
// An event no currently-bound listener matches is released VACUOUSLY (the
// loop finds nothing unsettled) — the same vacuous reading retainedLocked's
// second half uses for retention, so an orphan serialize-marked event never
// occupies a slot it could never be delivered through anyway.
//
// Caller holds q.mu.
func (q *Queue) releasedLocked(e *entry) bool {
	for _, ls := range q.listeners {
		if ls.l.Matches(e.evt) && !e.settled[ls.l.ID()] {
			return false
		}
	}
	return true
}

// headFor returns the head deliverable event for a listener: the earliest (by
// enqueue order) event that matches the listener's binding and that the listener
// is still owed an attempt on. Per-listener serial FIFO with head-of-line
// blocking (ADR 0031): only the head is offered; a declined head is re-offered
// until accepted, or until an attempt made past `expiresAt` settles it.
//
// Head selection is deliberately EXPIRY-BLIND. Under INV-EVT-4 the expiry check
// happens AT ATTEMPT TIME and decides whether that attempt is the last one — it
// is not a filter on whether to attempt at all. Skipping expired events here (as
// a duration-bounded queue could) would drop the born-expired default's only
// opportunity un-offered, violating INV-EVT-1.
//
// SERIALIZE OCCUPANCY (INV-CONC-1, active only for a type in q.serializeTypes).
// The scan additionally tracks, per marked TYPE, whether an earlier entry of
// that type already occupies the slot for the WHOLE scan — across every
// listener, not just l. The first not-yet-RELEASED entry of a marked type
// encountered occupies it; every LATER entry of that same type is skipped
// outright for the rest of this scan, even one that otherwise matches l and is
// not settled for l, until the occupant is released (releasedLocked). This is
// why the occupancy bookkeeping runs unconditionally over every entry of a
// marked type passed, not only ones that reach l's own settled/Matches checks
// below — occupancy is a property of the TYPE, not of which listener is asking,
// so every call to headFor (one per listener, per Dispatch pass) independently
// re-derives the same answer from the same entries.
//
// Caller holds q.mu.
func (q *Queue) headFor(l Listener) *entry {
	var occupied map[string]bool // lazily built only when serialize marks exist
	if len(q.serializeTypes) > 0 {
		occupied = map[string]bool{}
	}
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			continue // evicted
		}
		if occupied != nil && q.serializeTypes[e.evt.Type] {
			if occupied[e.evt.Type] {
				continue // an earlier, still-unreleased same-type entry occupies the slot
			}
			if !q.releasedLocked(e) {
				occupied[e.evt.Type] = true // this entry is now the type's occupant
			}
		}
		if e.settled[l.ID()] {
			continue // accepted, or its final attempt is already made
		}
		if !l.Matches(e.evt) {
			continue // not bound
		}
		return e
	}
	return nil
}

// pendingOffer is one (listener, event) attempt for this pass — a snapshot taken
// under q.mu so the listener callback (Listener.Offer) runs UNLOCKED.
type pendingOffer struct {
	ls  *listenerState
	evt Event // value copy: Offer never sees queue-internal state
	// id is this attempt's dispatch tracking id (Task 2.2): dsp-<12 hex>,
	// minted before q.mu was taken for this pass (see Dispatch). It is the
	// Offering.ID handed to Listener.Offer and the Queue.custody key for the
	// pass's phase 2.
	id string
	// lastAttempt is the INV-EVT-4 decision for THIS attempt: the event was
	// already expired when the attempt was made, so accept or decline, the core
	// never offers it to this listener again. It is evaluated once, from the
	// snapshot's clock reading — the last reading before the offer — and carried
	// forward rather than recomputed, so one attempt cannot be judged against two
	// different "now"s.
	lastAttempt bool
	// result is what Offer returned (filled in by the offer phase).
	result OfferResult
	// dispatchFailed is true when the offer phase never got a graceful
	// accept/decline reply out of Offer at all — currently, a recovered panic
	// (see offerSafely). It is meaningless when result.Accepted is true. This
	// is what lets the record phase tell INV-OBS-1's two delivery-side
	// failure classes apart even though both currently share the same
	// result.Accepted==false shape.
	dispatchFailed bool
}

// signalKind classifies one dispatchSignal (Task 2.3, Step 2.3.5). Dispatch's
// phase 3 is the only producer today — Enqueue and Expire each fire their
// own (single-kind) OnDeduped/OnUnconsumedExpired signal directly after their
// own unlock, since neither ever needs to mix signal kinds within one pass.
type signalKind int

const (
	signalAccept signalKind = iota
	signalDeclined
	// signalDispatchFailure carries offerSafely's recovered-panic class
	// (INV-OBS-1's OTHER delivery-side failure, bead pg2-icm3u) — distinct
	// from signalDeclined so it fans out via OnDispatchFailure, never
	// OnDeclined, and never touches the declined counters (Task 2.3, Step
	// 2.3.6): a dispatch failure is not a graceful decline.
	signalDispatchFailure
)

// dispatchSignal is one observer notification Dispatch's phase 3 queues while
// q.mu is held and fans out AFTER releasing it (Task 2.3, Step 2.3.5) —
// never synchronously while locked, per the lock-order invariant on q.mu's
// own doc. It is pass-local: built fresh by each Dispatch call, never
// retained across calls.
type dispatchSignal struct {
	kind     signalKind
	evtType  string
	eventID  string
	listener string
	reason   DeclineReason
}

// fanOut delivers each queued signal to q.obs, in the order phase 3 recorded
// them. Callers MUST call this only AFTER releasing q.mu — the whole point
// of collecting signals into sigs in the first place.
func (q *Queue) fanOut(sigs []dispatchSignal) {
	for _, s := range sigs {
		switch s.kind {
		case signalAccept:
			q.obs.OnAccept(s.eventID, s.listener)
		case signalDeclined:
			q.obs.OnDeclined(s.evtType, s.listener, s.reason.String())
		case signalDispatchFailure:
			q.obs.OnDispatchFailure(s.evtType)
		}
	}
}

// newDispatchID mints a fresh dispatch tracking id: "dsp-" followed by 12 hex
// characters of crypto/rand entropy — the same token-minting pattern
// internal/core/socket.go's newToken uses (crypto/rand + encoding/hex), at a
// shorter length: nothing here needs socket.go's full 32-byte auth-token
// entropy budget, since a dispatch id only has to be practically unique for
// the life of one in-flight offer, never secret. These values fill
// deliveries[].id (Task 0.4) once dispatch surfaces through status.
func newDispatchID() (string, error) {
	b := make([]byte, 6) // 6 bytes -> 12 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("eventqueue: mint dispatch id: %w", err)
	}
	return "dsp-" + hex.EncodeToString(b), nil
}

// offerSafely calls l.Offer(o), recovering a panic from the listener's own
// implementation so that one bad handler cannot take down the whole Dispatch
// pass — and, transitively, every OTHER registered listener's delivery this
// pass (INV-PREC-1: safety/isolation ranks above continuity, which ranks
// above efficiency). A recovered panic is reported as a genuine dispatch
// failure (INV-OBS-1's "the core could not hand the event over at all") —
// this IS a case of that: the offer never produced a reply at all, graceful
// or otherwise. The panic value and a stack trace are logged with the
// listener and event ids so the underlying bug stays diagnosable; Dispatch's
// RECORD phase then settles the pair exactly as it would a graceful decline
// (see Dispatch's own doc), just under the other metric class.
func offerSafely(l Listener, o Offering) (result OfferResult, dispatchFailed bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("eventqueue: listener Offer panicked; treating as a dispatch failure (INV-OBS-1)",
				"listenerId", l.ID(), "eventId", o.Event.ID, "eventType", o.Event.Type,
				"panic", r, "stack", string(debug.Stack()))
			result, dispatchFailed = OfferResult{Accepted: false}, true
		}
	}()
	return l.Offer(o), false
}

// Dispatch offers each listener its head deliverable event once, in
// registration order, and returns how many events were accepted this pass. A
// pre-accept decline OR a recovered dispatch-failure panic (offerSafely) on an
// UNEXPIRED event leaves the head in place for a later pass (re-offer,
// INV-FAIL-1 / INV-CONC-1); either one on an ALREADY-EXPIRED event was that
// listener's last attempt (INV-EVT-4), so the pair is settled and the head
// advances. The two are handled identically here and differ only in which
// Observer hook — and so which INV-OBS-1 failure-rate class — records them
// (bead pg2-icm3u).
//
// Dispatch ids (Task 2.2) are minted BEFORE q.mu is taken for the pass: a pass
// offers each registered listener at most once, so listenerCount — a lock-free
// atomic mirror of len(q.listeners), maintained by Register — is always a
// sufficient supply. Minting the whole batch up front keeps every crypto/rand
// call outside every lock this function takes; the one fallback below (for a
// listener registered in the narrow window between the mint and phase 1's
// lock) mints on the spot but is, itself, still outside q.mu.
//
// Locking discipline (bead pg2-56186). The pass is three phases and the queue
// lock is held only in phases 1 and 3, NEVER across the listener callback:
//
//  1. SNAPSHOT (locked): compute each listener's head deliverable event, capture
//     the (listener, event) pairs and the INV-EVT-4 expiry verdict for each,
//     assign each one its pre-minted dispatch id, and record a custody entry
//     for it (Task 2.2) — the offer is now OUTSTANDING. No Offer, no store
//     write here.
//  2. OFFER (UNLOCKED): call Listener.Offer for each pair, passing its dispatch
//     id, via offerSafely so a panicking listener implementation cannot abort
//     the pass (INV-PREC-1; see offerSafely's own doc). Releasing the lock is
//     what makes a synchronous listener's accept path free to re-enter the
//     queue (Enqueue / push-inject a follow-on event) without self-deadlocking
//     on the non-reentrant q.mu, and stops all ingest from serializing behind
//     an in-flight (possibly long) handler offer.
//  3. RECORD (locked): delete each pair's custody entry FIRST — the offer is no
//     longer outstanding whatever its outcome, including one whose underlying
//     entry vanished mid-offer and so never reaches the settle logic below at
//     all — then re-validate against CURRENT state and settle the pair:
//     marking acceptance, appending the durable opAccept record, notifying the
//     observer and maybe-evicting, or (for a final decline or dispatch
//     failure) recording nothing but the terminal marker.
//
// Between phases 2 and 3 the queue can change (concurrent Enqueue / Expire /
// Dispatch, or a re-entrant call from inside Offer), so RECORD looks the entry
// up FRESH by id and skips two ways rather than mutating a stale snapshot:
//   - entry no longer present (retired and swept, or early-evicted): the event
//     has legitimately left the queue, so there is nothing to record and
//     nothing to redeliver; drop the acceptance record (a stray opAccept for a
//     gone id is a no-op on replay anyway). Delivery is unaffected — the listener
//     already took responsibility when Offer returned Accepted=true (INV-EVT-1).
//   - already accepted by this listener (a concurrent/re-entrant Dispatch offered
//     the same head and recorded first): skip, preserving at-most-once acceptance
//     per (event, listener) binding — the duplicate Offer is absorbed by the
//     idempotent-listener contract (INV-EVT-2).
//
// The store append(s) stay under q.mu in phase 3 on purpose: the Store is not
// internally synchronized (its writes are serialized solely by q.mu), so moving
// them out would introduce a data race. Phase 3 is short and calls no listener
// code, unlike the original monolithic pass that held the lock across every
// Offer. Every accept record still persists via its own Append call (delivery
// depends on that ordering, see below); every early-eviction record this pass
// produces (maybeEvict, opt-in via WithEarlyEviction) is instead collected and
// persisted once, after the loop, via a single AppendBatch call — one fsync for
// the whole pass's evictions rather than one per evicted id.
func (q *Queue) Dispatch() (accepted int) {
	// Dispatch ids minted BEFORE q.mu is taken (see doc above): a batch sized
	// to the current listener count, read without a lock.
	ids := make([]string, q.listenerCount.Load())
	for i := range ids {
		id, err := newDispatchID()
		if err != nil {
			// crypto/rand failure is unrecoverable in line; degrade the same
			// way a swallowed durable write does elsewhere in this file (log
			// and continue) rather than panicking a whole dispatch pass.
			slog.Error("eventqueue: mint dispatch id failed", "err", err)
		}
		ids[i] = id
	}
	nextID := 0
	takeID := func() string {
		if nextID < len(ids) {
			id := ids[nextID]
			nextID++
			return id
		}
		// A listener registered in the narrow window between the mint above
		// and phase 1's lock below. Minting here is still outside q.mu.
		id, err := newDispatchID()
		if err != nil {
			slog.Error("eventqueue: mint dispatch id failed", "err", err)
		}
		return id
	}

	// Phase 1 — SNAPSHOT (locked).
	q.mu.Lock()
	now := q.now()
	pending := make([]pendingOffer, 0, len(q.listeners))
	for _, ls := range q.listeners {
		e := q.headFor(ls.l)
		if e == nil {
			continue
		}
		if !ls.eligibleNow(e.evt.ID, now) {
			continue // still cooling down from a prior pre-accept decline (INV-FAIL-2)
		}
		id := takeID()
		q.custody[id] = custody{}
		pending = append(pending, pendingOffer{ls: ls, evt: e.evt, id: id, lastAttempt: e.evt.Expired(now)})
	}
	q.mu.Unlock()
	if len(pending) == 0 {
		return 0
	}

	// Phase 2 — OFFER (UNLOCKED). ls.l is set once at Register and never mutated,
	// so reading it here without the lock is safe. offerSafely recovers a
	// panicking Offer so one listener's bug cannot abort the rest of this
	// pass's offers (see its own doc).
	for i := range pending {
		pending[i].result, pending[i].dispatchFailed = offerSafely(pending[i].ls.l, Offering{ID: pending[i].id, Event: pending[i].evt})
	}

	// Phase 3 — RECORD (locked), re-validating each outcome against current state
	// (see the locking-discipline note above).
	//
	// Task 2.3 (Steps 2.3.5 + 2.3.6) adds two things to this phase, both
	// staying strictly under the ALREADY-HELD q.mu:
	//   - per-listener delivered/declined counters (p.ls.delivered/declined)
	//     and the pool-wide atomic counterparts (q.delivered/q.declined) —
	//     incremented HERE, never from an observer hook, never behind a
	//     separate mutex;
	//   - every OnAccept/OnDeclined observer notification is queued into
	//     `signals` instead of firing inline, and fanned out ONLY after q.mu
	//     is released below (panic-safe unlockOnce) — the lock-order
	//     invariant on q.mu's own doc forbids calling into an observer
	//     while holding it.
	q.mu.Lock()
	unlock := q.unlockOnce()
	defer unlock()
	var evicts []Record
	var signals []dispatchSignal
	for _, p := range pending {
		// The pass's phase 2 for this offer has concluded either way — delete
		// custody FIRST, before any of the skip/settle branches below, so an
		// entry that never reaches settlement (e.g. gone by the time this
		// loop reaches it) still leaves custody accurately empty.
		delete(q.custody, p.id)
		lid := p.ls.l.ID()
		e, ok := q.entries[p.evt.ID]
		if !ok {
			continue // entry left the queue mid-dispatch (retired/evicted): skip
		}
		if !p.result.Accepted {
			// A PRE-ACCEPT decline OR a dispatch failure (offerSafely recovered a
			// panic) — INV-OBS-1's two delivery-side classes. Nothing DURABLE about
			// the attempt is recorded — no counter, nothing on disk (DEC-EVENT-1:
			// the core keeps no attempt history) — but it IS a delivery-side
			// failure signal (INV-OBS-1 / INV-FAIL-1), so the observer sees every
			// occurrence regardless of lastAttempt (see OnDeclined's doc), whatever
			// the DeclineReason. Both classes settle IDENTICALLY from here: the
			// single expiry comparison already made in phase 1 is the whole
			// retention decision: past `expiresAt` that attempt was the last one
			// this listener is owed (INV-EVT-4), so settle the pair and let its
			// head advance; before it, the failure is simply a re-offer condition
			// (INV-FAIL-1) — every DeclineReason re-offers identically — and the
			// IN-MEMORY (transient, unpersisted) retry-cadence bookkeeping advances
			// so the next offer waits at least INV-FAIL-2's cadence rather than the
			// very next Dispatch pass. Only which Observer hook fires — and so
			// which failure-rate metric class counts it — differs.
			if p.dispatchFailed {
				signals = append(signals, dispatchSignal{kind: signalDispatchFailure, evtType: p.evt.Type})
			} else {
				p.ls.declined++
				q.declined.Add(1)
				signals = append(signals, dispatchSignal{kind: signalDeclined, evtType: p.evt.Type, listener: lid, reason: p.result.Decline})
			}
			if p.lastAttempt {
				e.settled[lid] = true
				p.ls.resetBackoff() // nothing left to back off from once settled
			} else {
				p.ls.recordDecline(p.evt.ID, now, q.retryBackoffFor(p.ls.l))
			}
			continue
		}
		if e.accepted[lid] {
			continue // already recorded by a concurrent/re-entrant pass: at-most-once
		}
		// Accepted: nothing left to back off from (INV-FAIL-2's cadence is moot
		// once the pair is settled).
		p.ls.resetBackoff()
		// In-memory accept first; the durable accept record is written AFTER
		// (ADR 0031 req 4) — the crash window that yields the at-least-once
		// redelivery (one extra re-offer per crash window).
		e.accepted[lid] = true
		e.settled[lid] = true
		if err := q.store.Append(Record{Op: opAccept, EventID: p.evt.ID, ListenerID: lid}); err != nil {
			// The in-memory accept already happened and the listener has taken
			// delivery responsibility (INV-EVT-1); we do NOT roll back or change
			// delivery semantics. But a swallowed accept-write is a durability
			// degradation — the event replays as un-accepted on EVERY restart and
			// is redelivered forever with no signal. Enqueue surfaces its append
			// error by returning it; Dispatch cannot return here without altering
			// delivery, so it surfaces the failure via a structured log instead
			// (matching the codebase's slog convention for a swallowed durable
			// write, cf. orchestrator "event log emit failed").
			slog.Error("eventqueue: accept-append failed; event will redeliver on restart until the write succeeds",
				"eventId", p.evt.ID, "listenerId", lid, "err", err)
		}
		p.ls.delivered++
		q.delivered.Add(1)
		signals = append(signals, dispatchSignal{kind: signalAccept, eventID: p.evt.ID, listener: lid})
		accepted++
		if rec, evicted := q.maybeEvict(e); evicted {
			evicts = append(evicts, rec)
		}
	}
	if len(evicts) > 0 {
		if err := q.store.AppendBatch(evicts); err != nil {
			// Matching the accept-append precedent above: the in-memory eviction(s)
			// already happened and delivery is unaffected, but a swallowed
			// evict-append is a durability degradation — the evicted id(s) replay as
			// retained and are re-offered after a restart until the write succeeds.
			slog.Error("eventqueue: evict-append failed; the event(s) will replay as retained and be re-offered after a restart",
				"count", len(evicts), "err", err)
		}
	}
	unlock()
	q.fanOut(signals)
	return accepted
}

// maybeEvict evicts an event early when opted-in and every currently-bound
// listener has accepted it (ADR 0031). Returns the durable evict Record and true
// when an eviction happened, so the caller (Dispatch's phase 3) can collect it
// into that pass's single AppendBatch call instead of persisting here — matching
// Expire's per-pass batching (one fsync per pass instead of one per record).
// Caller holds q.mu.
func (q *Queue) maybeEvict(e *entry) (Record, bool) {
	if !q.evictWhenAllAccept {
		return Record{}, false
	}
	for _, ls := range q.listeners {
		if ls.l.Matches(e.evt) && !e.accepted[ls.l.ID()] {
			return Record{}, false // a bound listener has not accepted yet
		}
	}
	rec := q.recordEvictLocked(e.evt.ID)
	delete(q.entries, e.evt.ID)
	q.publishCellLocked(e.evt.Type, "", "")
	// Drop the evicted id from the FIFO spine too. Leaving it as a tombstone lets
	// a re-emit BEFORE the next Expire() append a SECOND spine entry (the stale-
	// removal branch in Enqueue only fires while q.entries still holds the id),
	// which double-counts the id (INV-OBS-1) and reorders delivery (ADR-0031
	// req 1). Bead pg2-f8btt.
	q.dropFromOrder(e.evt.ID)
	return rec, true
}

// retireLocked removes an entry whose RETENTION IS OVER from q.entries and
// returns the durable opEvict Record so a replay does not resurrect it, along
// with whether it was an unconsumed-expired MISS (no listener ever accepted
// it, INV-DISP-3 / INV-OBS-1) — a signal a never-settled custody entry (Task
// 2.2) also funnels through, since an entry retired mid-offer never reaches
// Dispatch's own settle logic. Persisting the record and firing the observer
// hook are both the CALLER's responsibility: persistence is batched with the
// rest of the current pass into one AppendBatch call (one fsync per pass
// instead of one per record), and the observer fires from the returned
// signal, still synchronously and still under the SAME lock as before
// (today's timing is unchanged) — preparatory plumbing (Task 2.2) so a future
// caller can instead collect the signal into a pass-local list and fan it out
// AFTER releasing q.mu (Task 2.3's panic-safe-unlock restructuring) without
// retireLocked's own signature changing again. This function itself makes no
// Store call. The caller fixes up the FIFO spine — Expire rebuilds the whole
// spine in one pass, Enqueue drops the single stale id — so this does not
// touch q.order.
//
// Caller holds q.mu.
func (q *Queue) retireLocked(e *entry) (rec Record, unconsumedExpired bool) {
	unconsumedExpired = len(e.accepted) == 0
	rec = q.recordEvictLocked(e.evt.ID)
	delete(q.entries, e.evt.ID)
	q.publishCellLocked(e.evt.Type, "", "")
	return rec, unconsumedExpired
}

// recordEvictLocked returns the durable opEvict Record marking id as gone from
// the queue. It has no side effect on q.entries/q.order and makes no Store call:
// persisting the record — and, on Enqueue's stale-retire path, removing id from
// q.entries — is the caller's responsibility, since persistence is now batched
// (one AppendBatch call per pass in Dispatch/Expire, or per stale re-emit in
// Enqueue) rather than one Append per record. Caller holds q.mu.
func (q *Queue) recordEvictLocked(id string) Record {
	return Record{Op: opEvict, EventID: id}
}

// publishCellLocked atomically publishes a new depthCell derived from the one
// currently published: decType's retained count -1 (if non-empty, deleting
// the key rather than leaving a zero so DepthByType agrees with a fresh
// per-type scan that would never produce a zero-count entry), incType's count
// +1 (if non-empty), and seenType added to everSeen (if non-empty and not
// already present — add-only, INV: nothing ever deletes from everSeen). It
// never mutates the maps of the cell currently published (copy-on-write), so
// a concurrent lock-free DepthByType/UnmatchedBindings reader never observes
// a half-applied update — only ever the pre- or the fully post-mutation
// state. Passing decType == incType (a stale-retire re-emit of the same type)
// nets to no depth change, exactly as a full recompute would show. Caller
// holds q.mu.
func (q *Queue) publishCellLocked(decType, incType, seenType string) {
	cur := q.cell.Load()
	depth := cur.depth
	if decType != "" || incType != "" {
		next := make(map[string]int, len(cur.depth))
		for k, v := range cur.depth {
			next[k] = v
		}
		if decType != "" {
			if next[decType] <= 1 {
				delete(next, decType)
			} else {
				next[decType]--
			}
		}
		if incType != "" {
			next[incType]++
		}
		depth = next
	}
	everSeen := cur.everSeen
	if seenType != "" {
		if _, ok := cur.everSeen[seenType]; !ok {
			next := make(map[string]struct{}, len(cur.everSeen)+1)
			for k := range cur.everSeen {
				next[k] = struct{}{}
			}
			next[seenType] = struct{}{}
			everSeen = next
		}
	}
	q.cell.Store(&depthCell{depth: depth, everSeen: everSeen})
}

// Expire drops every event whose retention is over and returns how many were
// dropped. Retention is NOT the expiry instant alone: an event stays until every
// matching handler has had the one attempt it is owed (retainedLocked), which is
// why an event that expires with NO listener having accepted it is a genuine
// unconsumed-expired miss (INV-DISP-3 / INV-OBS-1) rather than a scheduling
// artifact. Retention is independent of consumer HEALTH: an event is never
// dropped merely because a consumer is down or disabled — such a consumer just
// leaves its events to expire unconsumed.
//
// Every eviction this pass produces persists as one AppendBatch call after the
// loop, rather than one Append (one fsync) per record. A batch failure is
// logged — matching the accept-append precedent in Dispatch — and does not
// block or roll back the pass: the in-memory drops already happened, same as an
// individual evict-append failure was already swallowed before this task.
//
// Task 2.3 (Step 2.3.5): every OnUnconsumedExpired signal this sweep finds is
// collected into a pass-local slice and fanned out AFTER q.mu is released
// (panic-safe unlockOnce), never synchronously while locked — the mirror of
// Enqueue's own stale-retire handling and Dispatch phase 3's fan-out below.
func (q *Queue) Expire() (dropped int) {
	q.mu.Lock()
	unlock := q.unlockOnce()
	defer unlock()
	now := q.now()
	kept := q.order[:0:0]
	var evicts []Record
	var misses []string // evt types of unconsumed-expired misses this sweep found
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			continue // already evicted
		}
		if q.retainedLocked(e, now) {
			kept = append(kept, id)
			continue
		}
		rec, unconsumedExpired := q.retireLocked(e)
		evicts = append(evicts, rec)
		if unconsumedExpired {
			misses = append(misses, e.evt.Type)
		}
		dropped++
	}
	q.order = kept
	if len(evicts) > 0 {
		if err := q.store.AppendBatch(evicts); err != nil {
			slog.Error("eventqueue: evict-append failed; the event(s) will replay as retained and be re-offered after a restart",
				"count", len(evicts), "err", err)
		}
	}
	unlock()
	for _, t := range misses {
		q.obs.OnUnconsumedExpired(t)
	}
	return dropped
}

// Idle reports whether no (event, listener) pair is still deliverable — every
// enqueued event is settled for each bound listener or is past its expiry with
// nothing owed — the condition run-until-idle exits on (INV-LIFE-1). Caller must
// NOT hold q.mu.
func (q *Queue) Idle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for _, ls := range q.listeners {
		if q.headFor(ls.l) != nil {
			return false // a deliverable head is still outstanding (incl. fan-out)
		}
	}
	// An UNEXPIRED event that no one has accepted is still pending — an orphan
	// (no binding) or a disabled/absent consumer's event waiting to reach its
	// expiry. It is neither accepted nor expired, so the queue is NOT drained
	// (INV-LIFE-1).
	for _, id := range q.order {
		if e, ok := q.entries[id]; ok && !e.evt.Expired(now) && len(e.accepted) == 0 {
			return false
		}
	}
	return true
}

// DepthByType returns the per-type count of RETAINED events — the "queue depth"
// gauge source (INV-OBS-1). It counts what is in the queue, expired or not,
// because under INV-EVT-4 "expired" no longer means "gone": a past-expiry event
// is still held, and still owed an attempt, until every matching handler has had
// one. Excluding those would hide real backlog — including, under the
// born-expired default, essentially all of it.
//
// Lock-free (Task 3.2): reads the depthCell published under q.mu at each of
// the four mutation sites (Enqueue's add, retireLocked, maybeEvict, replay's
// opEnqueue/opEvict handling) instead of scanning q.entries under lock. This
// is why internal/discover.produce's threshold-cascade fixpoint and
// cmd/pr-pool/run.go's metrics gauge closure — both existing callers — can
// call it as often as they like without contending on q.mu, and cannot
// self-deadlock even if a future caller reached it while already holding
// q.mu. The returned map is the cell's own map, immutable by convention —
// callers MUST NOT mutate it. Caller must NOT hold q.mu (though, per the
// above, holding it would no longer be harmful either).
func (q *Queue) DepthByType() map[string]int {
	return q.cell.Load().depth
}

// UnmatchedBindings returns every type in declared that this run's queue has
// NEVER enqueued — a lock-free read of the cell's everSeen set (add-only:
// eviction/expiry never un-sees a type once it has been enqueued at least
// once). declared is the caller's own declared-types list (e.g.
// core.Bindings); this method does not resolve where that list comes from —
// see Binding Decision 2. Order follows declared, not everSeen's own
// (unordered) iteration. Caller must NOT hold q.mu.
func (q *Queue) UnmatchedBindings(declared []string) []string {
	seen := q.cell.Load().everSeen
	var unmatched []string
	for _, t := range declared {
		if _, ok := seen[t]; !ok {
			unmatched = append(unmatched, t)
		}
	}
	return unmatched
}

// SessionsInFlight reports how many offers are currently outstanding in phase
// 2 of a dispatch pass (custody, Task 2.2) — the eventual "N in flight" the
// status banner surfaces (Task 3.0). It is read LIVE under q.mu at call time,
// the same as DepthByType, and is NEVER cached in a periodic snapshot:
// querying it while a listener's Offer call is blocked mid-pass must observe
// that offer. Before Phase 5's deferred-settle form this is always 0 or 1,
// since Dispatch offers one listener at a time, synchronously, within a
// single goroutine; a later deferred form is what lets it grow past 1. Caller
// must NOT hold q.mu.
func (q *Queue) SessionsInFlight() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.custody)
}

// RunUntilIdle dispatches and expires on a fixed tick until the queue is idle
// (INV-LIFE-1) or ctx is cancelled. It is the drive loop behind the
// `run-until-idle` operator subcommand; a busy handler simply keeps its head
// re-offered until the head is accepted or the re-offer window `expiresAt` bounds
// closes.
//
// DISPATCH RUNS BEFORE EXPIRE, and the order is load-bearing under INV-EVT-4.
// Every event is owed an attempt before it may be dropped (INV-EVT-1), and the
// default event is born expired — so sweeping first would just retain it
// (retainedLocked holds it for the outstanding attempt) and cost a whole extra
// tick. Dispatching first makes the attempt, and the sweep that follows in the
// SAME pass retires the event the attempt settled, so `run-until-idle` drains a
// default workload in one pass instead of two.
//
// The between-pass wait blocks on the `after` seam (WithSleeper), NOT directly
// on time.After, so it advances on the SAME clock the expiry math uses. Under the
// real default this genuinely waits `tick`; under a mock clock whose `after`
// advances virtual time, the loop makes progress toward `expiresAt` and
// terminates deterministically without real sleeping (and without the
// frozen-clock busy-loop a real time.After caused when q.now never advanced).
func (q *Queue) RunUntilIdle(ctx context.Context, tick time.Duration) error {
	for {
		q.Dispatch()
		q.Expire()
		if q.Idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-q.after(tick):
		}
	}
}
