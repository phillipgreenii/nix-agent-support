package eventqueue

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
)

// Listener is a bound event handler as the queue sees it (INTF-HANDLER, core
// side). It declares which events it binds (Matches, INV-DISP-1) and accepts or
// declines an offer (Offer). A return of accepted=false is a PRE-ACCEPT decline
// (busy / unavailable, INV-CONC-1 / INV-FAIL-1): the core re-offers the event
// while it is unexpired, at the cadence INV-FAIL-2 defines (WithRetryBackoff, or
// a per-listener override via BackoffListener). Once Offer returns true the
// event is ACCEPTED and the core's delivery responsibility ends (INV-EVT-1);
// post-accept retry/resume is the handler's.
type Listener interface {
	ID() string
	Matches(evt Event) bool
	Offer(evt Event) (accepted bool)
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
	OnDeclined(evtType string)
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
}

type noopObserver struct{}

func (noopObserver) OnEnqueue(Event)            {}
func (noopObserver) OnAccept(string, string)    {}
func (noopObserver) OnUnconsumedExpired(string) {}
func (noopObserver) OnDeclined(string)          {}
func (noopObserver) OnDispatchFailure(string)   {}

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

// Queue is the durable, ordered, de-duped, retention-bounded event queue
// (ADR 0031, expiry bound amended by DEC-EVENT-1). It is safe for concurrent use.
type Queue struct {
	mu  sync.Mutex
	now func() time.Time
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
}

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
	}
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
			}
			q.entries[e.evt.ID] = e
		case opAccept:
			if e, ok := q.entries[r.EventID]; ok {
				e.accepted[r.ListenerID] = true
				e.settled[r.ListenerID] = true
			}
		case opEvict:
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
func (q *Queue) Enqueue(evt Event) (EnqueueResult, error) {
	if err := evt.Validate(); err != nil {
		return Enqueued, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	evt = evt.Resolve(now)
	enqueueRecord := recordFromEvent(evt, now)
	if e, ok := q.entries[evt.ID]; ok {
		if q.retainedLocked(e, now) {
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
		if len(e.accepted) == 0 {
			q.obs.OnUnconsumedExpired(e.evt.Type)
		}
		delete(q.entries, e.evt.ID)
		q.dropFromOrder(evt.ID)
		q.entries[evt.ID] = newEntry(evt)
		q.order = append(q.order, evt.ID)
		q.obs.OnEnqueue(evt)
		return Enqueued, nil
	}
	if err := q.store.Append(enqueueRecord); err != nil {
		return Enqueued, err
	}
	q.entries[evt.ID] = newEntry(evt)
	q.order = append(q.order, evt.ID)
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
	// lastAttempt is the INV-EVT-4 decision for THIS attempt: the event was
	// already expired when the attempt was made, so accept or decline, the core
	// never offers it to this listener again. It is evaluated once, from the
	// snapshot's clock reading — the last reading before the offer — and carried
	// forward rather than recomputed, so one attempt cannot be judged against two
	// different "now"s.
	lastAttempt bool
	// accepted is what Offer returned (filled in by the offer phase).
	accepted bool
	// dispatchFailed is true when the offer phase never got a graceful
	// accept/decline reply out of Offer at all — currently, a recovered panic
	// (see offerSafely). It is meaningless when accepted is true. This is what
	// lets the record phase tell INV-OBS-1's two delivery-side failure classes
	// apart even though both currently share the same accepted==false shape.
	dispatchFailed bool
}

// offerSafely calls l.Offer(evt), recovering a panic from the listener's own
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
func offerSafely(l Listener, evt Event) (accepted, dispatchFailed bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("eventqueue: listener Offer panicked; treating as a dispatch failure (INV-OBS-1)",
				"listenerId", l.ID(), "eventId", evt.ID, "eventType", evt.Type,
				"panic", r, "stack", string(debug.Stack()))
			accepted, dispatchFailed = false, true
		}
	}()
	return l.Offer(evt), false
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
// Locking discipline (bead pg2-56186). The pass is three phases and the queue
// lock is held only in phases 1 and 3, NEVER across the listener callback:
//
//  1. SNAPSHOT (locked): compute each listener's head deliverable event, capture
//     the (listener, event) pairs and the INV-EVT-4 expiry verdict for each. No
//     Offer, no store write here.
//  2. OFFER (UNLOCKED): call Listener.Offer for each pair, via offerSafely so a
//     panicking listener implementation cannot abort the pass (INV-PREC-1;
//     see offerSafely's own doc). Releasing the lock is what makes a
//     synchronous listener's accept path free to re-enter the queue
//     (Enqueue / push-inject a follow-on event) without self-deadlocking on the
//     non-reentrant q.mu, and stops all ingest from serializing behind an
//     in-flight (possibly long) handler offer.
//  3. RECORD (locked): for each outcome, re-validate against CURRENT state then
//     settle the pair — marking acceptance, appending the durable opAccept
//     record, notifying the observer and maybe-evicting, or (for a final decline
//     or dispatch failure) recording nothing but the terminal marker.
//
// Between phases 2 and 3 the queue can change (concurrent Enqueue / Expire /
// Dispatch, or a re-entrant call from inside Offer), so RECORD looks the entry
// up FRESH by id and skips two ways rather than mutating a stale snapshot:
//   - entry no longer present (retired and swept, or early-evicted): the event
//     has legitimately left the queue, so there is nothing to record and
//     nothing to redeliver; drop the acceptance record (a stray opAccept for a
//     gone id is a no-op on replay anyway). Delivery is unaffected — the listener
//     already took responsibility when Offer returned true (INV-EVT-1).
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
		pending = append(pending, pendingOffer{ls: ls, evt: e.evt, lastAttempt: e.evt.Expired(now)})
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
		pending[i].accepted, pending[i].dispatchFailed = offerSafely(pending[i].ls.l, pending[i].evt)
	}

	// Phase 3 — RECORD (locked), re-validating each outcome against current state
	// (see the locking-discipline note above).
	q.mu.Lock()
	defer q.mu.Unlock()
	var evicts []Record
	for _, p := range pending {
		lid := p.ls.l.ID()
		e, ok := q.entries[p.evt.ID]
		if !ok {
			continue // entry left the queue mid-dispatch (retired/evicted): skip
		}
		if !p.accepted {
			// A PRE-ACCEPT decline OR a dispatch failure (offerSafely recovered a
			// panic) — INV-OBS-1's two delivery-side classes. Nothing DURABLE about
			// the attempt is recorded — no counter, nothing on disk (DEC-EVENT-1:
			// the core keeps no attempt history) — but it IS a delivery-side
			// failure signal (INV-OBS-1 / INV-FAIL-1), so the observer sees every
			// occurrence regardless of lastAttempt (see OnDeclined's doc). Both
			// classes settle IDENTICALLY from here: the single expiry comparison
			// already made in phase 1 is the whole retention decision: past
			// `expiresAt` that attempt was the last one this listener is owed
			// (INV-EVT-4), so settle the pair and let its head advance; before it,
			// the failure is simply a re-offer condition (INV-FAIL-1), and the
			// IN-MEMORY (transient, unpersisted) retry-cadence bookkeeping advances
			// so the next offer waits at least INV-FAIL-2's cadence rather than the
			// very next Dispatch pass. Only which Observer hook fires — and so
			// which failure-rate metric class counts it — differs.
			if p.dispatchFailed {
				q.obs.OnDispatchFailure(p.evt.Type)
			} else {
				q.obs.OnDeclined(p.evt.Type)
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
		q.obs.OnAccept(p.evt.ID, lid)
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
	// Drop the evicted id from the FIFO spine too. Leaving it as a tombstone lets
	// a re-emit BEFORE the next Expire() append a SECOND spine entry (the stale-
	// removal branch in Enqueue only fires while q.entries still holds the id),
	// which double-counts the id (INV-OBS-1) and reorders delivery (ADR-0031
	// req 1). Bead pg2-f8btt.
	q.dropFromOrder(e.evt.ID)
	return rec, true
}

// retireLocked removes an entry whose RETENTION IS OVER from q.entries: it counts
// the miss when no listener ever accepted it (unconsumed-expired, INV-DISP-3 /
// INV-OBS-1), removes it, and returns the durable opEvict Record so a replay does
// not resurrect it. Persisting the record is the CALLER's responsibility, batched
// with the rest of the current pass into one AppendBatch call (one fsync per
// pass instead of one per record) — this function itself makes no Store call.
// The caller fixes up the FIFO spine — Expire rebuilds the whole spine in one
// pass, Enqueue drops the single stale id — so this does not touch q.order.
//
// Caller holds q.mu.
func (q *Queue) retireLocked(e *entry) Record {
	if len(e.accepted) == 0 {
		q.obs.OnUnconsumedExpired(e.evt.Type)
	}
	rec := q.recordEvictLocked(e.evt.ID)
	delete(q.entries, e.evt.ID)
	return rec
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
func (q *Queue) Expire() (dropped int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	kept := q.order[:0:0]
	var evicts []Record
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			continue // already evicted
		}
		if q.retainedLocked(e, now) {
			kept = append(kept, id)
			continue
		}
		evicts = append(evicts, q.retireLocked(e))
		dropped++
	}
	q.order = kept
	if len(evicts) > 0 {
		if err := q.store.AppendBatch(evicts); err != nil {
			slog.Error("eventqueue: evict-append failed; the event(s) will replay as retained and be re-offered after a restart",
				"count", len(evicts), "err", err)
		}
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
// born-expired default, essentially all of it. Caller must NOT hold q.mu.
func (q *Queue) DepthByType() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	depth := map[string]int{}
	for _, id := range q.order {
		if e, ok := q.entries[id]; ok {
			depth[e.evt.Type]++
		}
	}
	return depth
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
