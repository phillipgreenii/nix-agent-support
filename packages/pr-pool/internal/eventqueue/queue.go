package eventqueue

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Listener is a bound event handler as the queue sees it (INTF-HANDLER, core
// side). It declares which events it binds (Matches, INV-DISP-1) and accepts or
// declines an offer (Offer). A return of accepted=false is a PRE-ACCEPT decline
// (busy / unavailable, INV-CONC-1 / INV-FAIL-1): the core re-offers within the
// ttl. Once Offer returns true the event is ACCEPTED and the core's delivery
// responsibility ends (INV-EVT-1); post-accept retry/resume is the handler's.
type Listener interface {
	ID() string
	Matches(evt Event) bool
	Offer(evt Event) (accepted bool)
}

// Observer receives queue lifecycle signals for metric emission (INV-OBS-1).
// The OTel emitter (bead pg2-hvlyj.18) implements it; the default is a no-op.
type Observer interface {
	OnEnqueue(evt Event)
	OnAccept(eventID, listenerID string)
	OnUnconsumedExpired(evtType string)
}

type noopObserver struct{}

func (noopObserver) OnEnqueue(Event)            {}
func (noopObserver) OnAccept(string, string)    {}
func (noopObserver) OnUnconsumedExpired(string) {}

// EnqueueResult reports whether an enqueue added a new event or was dropped as
// a duplicate of a still-retained id (INV-EVT-3).
type EnqueueResult int

const (
	// Enqueued means the event was newly appended to the durable queue.
	Enqueued EnqueueResult = iota
	// Deduped means an event with the same id is still retained within ttl, so
	// this re-emit was dropped (INV-EVT-3).
	Deduped
)

type entry struct {
	evt        Event
	enqueuedAt time.Time
	// accepted tracks acceptance per (event, listener) (INV-EVT-1). An entry is
	// retained until its ttl even after acceptance so a listener binding within
	// the ttl can still receive it.
	accepted map[string]bool
}

func (e *entry) deadline() time.Time { return e.enqueuedAt.Add(e.evt.TTL) }

type listenerState struct {
	l Listener
}

// Queue is the durable, ordered, de-duped, TTL-bounded event queue (ADR 0031).
// It is safe for concurrent use.
type Queue struct {
	mu  sync.Mutex
	now func() time.Time
	// after is the wait seam RunUntilIdle blocks on between passes (default
	// time.After). It is paired with the `now` clock seam so a mock clock can
	// drive BOTH coherently: a mock `after` advances virtual time by the tick and
	// fires immediately, so RunUntilIdle terminates on ttl deterministically
	// without real sleeping (see WithSleeper). The real default genuinely waits.
	after func(time.Duration) <-chan time.Time
	store Store
	obs   Observer

	entries map[string]*entry // by event id; the retained-until-ttl set
	order   []string          // enqueue order (event ids) — the FIFO spine

	listeners []*listenerState // registration order; stable per-listener cursors

	// evictWhenAllAccept is the opt-in early-eviction switch (ADR 0031). Default
	// off: keep every event until ttl. On: evict once all currently-bound
	// listeners have accepted (disk savings; shortens the dedup window — safe
	// only when the consumer set is fixed).
	evictWhenAllAccept bool
}

// Option configures a Queue.
type Option func(*Queue)

// WithClock injects a clock seam (default time.Now) for deterministic ttl tests.
func WithClock(now func() time.Time) Option { return func(q *Queue) { q.now = now } }

// WithSleeper injects the wait seam RunUntilIdle blocks on between passes
// (default time.After). It is the companion to WithClock: a mock clock supplies
// an `after` that ADVANCES its virtual time by the requested tick and fires
// immediately, so RunUntilIdle drains and terminates on ttl deterministically
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

// New constructs a Queue over store, replaying any prior durable state so
// delivery survives a restart (INV-EVT-1). Records for events already past
// their ttl are dropped on replay; evicted ids are not resurrected.
func New(store Store, opts ...Option) (*Queue, error) {
	q := &Queue{
		now:     time.Now,
		after:   time.After,
		store:   store,
		obs:     noopObserver{},
		entries: map[string]*entry{},
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
func (q *Queue) replay() error {
	recs, err := q.store.Replay()
	if err != nil {
		return err
	}
	now := q.now()
	for _, r := range recs {
		switch r.Op {
		case opEnqueue:
			e := &entry{evt: r.event(), enqueuedAt: r.EnqueuedAt, accepted: map[string]bool{}}
			if now.Before(e.deadline()) { // still within ttl
				if _, seen := q.entries[e.evt.ID]; !seen {
					q.order = append(q.order, e.evt.ID)
				}
				q.entries[e.evt.ID] = e
			}
		case opAccept:
			if e, ok := q.entries[r.EventID]; ok {
				e.accepted[r.ListenerID] = true
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
// (Validate). A re-emit of an id still retained within ttl is dropped as a
// duplicate (INV-EVT-3). The enqueue record is persisted BEFORE the in-memory
// add, so an accepted-then-crashed event is never lost.
func (q *Queue) Enqueue(evt Event) (EnqueueResult, error) {
	if err := evt.Validate(); err != nil {
		return Enqueued, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	if e, ok := q.entries[evt.ID]; ok {
		if now.Before(e.deadline()) {
			return Deduped, nil // still-retained duplicate id (INV-EVT-3)
		}
		// The id exists but has expired and not yet been swept. A re-emit is a
		// FRESH event and MUST go to the tail (FIFO), not reuse the stale
		// position — so evict the stale entry first.
		delete(q.entries, evt.ID)
		q.dropFromOrder(evt.ID)
	}
	if err := q.store.Append(recordFromEvent(evt, now)); err != nil {
		return Enqueued, err
	}
	q.entries[evt.ID] = &entry{evt: evt, enqueuedAt: now, accepted: map[string]bool{}}
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

// headFor returns the head deliverable event for a listener: the earliest (by
// enqueue order) event that matches the listener's binding, is not yet accepted
// by it, and has not expired. Per-listener serial FIFO with head-of-line
// blocking (ADR 0031): only the head is offered; a declined head is re-offered
// until accepted or expired. Caller holds q.mu.
func (q *Queue) headFor(l Listener, now time.Time) *entry {
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			continue // evicted
		}
		if !now.Before(e.deadline()) {
			continue // expired; Expire will drop it
		}
		if e.accepted[l.ID()] {
			continue // already accepted by this listener
		}
		if !l.Matches(e.evt) {
			continue // not bound
		}
		return e
	}
	return nil
}

// pendingOffer is one (listener, event) pair to offer this pass — a snapshot
// taken under q.mu so the listener callback (Listener.Offer) runs UNLOCKED.
type pendingOffer struct {
	ls  *listenerState
	evt Event // value copy: Offer never sees queue-internal state
}

// Dispatch offers each listener its head deliverable event once, in
// registration order, and returns how many events were accepted this pass. A
// busy pre-accept decline leaves the head for a later pass (re-offer within
// ttl, INV-FAIL-1 / INV-CONC-1).
//
// Locking discipline (bead pg2-56186). The pass is three phases and the queue
// lock is held only in phases 1 and 3, NEVER across the listener callback:
//
//  1. SNAPSHOT (locked): compute each listener's head deliverable event and
//     capture the (listener, event) pairs. No Offer, no store write here.
//  2. OFFER (UNLOCKED): call Listener.Offer for each pair. Releasing the lock is
//     what makes a synchronous listener's accept path free to re-enter the queue
//     (Enqueue / push-inject a follow-on event) without self-deadlocking on the
//     non-reentrant q.mu, and stops all ingest from serializing behind an
//     in-flight (possibly long) handler offer.
//  3. RECORD (locked): for each acceptance, re-validate against CURRENT state
//     then mark accepted, append the durable opAccept record, notify the
//     observer, and maybe-evict — reproducing the original per-acceptance
//     ordering (maybeEvict after each mark, in registration order).
//
// Between phases 2 and 3 the queue can change (concurrent Enqueue / Expire /
// Dispatch, or a re-entrant call from inside Offer), so RECORD looks the entry
// up FRESH by id and skips two ways rather than mutating a stale snapshot:
//   - entry no longer present (expired past ttl and swept, or early-evicted): the
//     event has legitimately left the queue, so there is nothing to record and
//     nothing to redeliver; drop the acceptance record (a stray opAccept for a
//     gone id is a no-op on replay anyway). Delivery is unaffected — the listener
//     already took responsibility when Offer returned true (INV-EVT-1).
//   - already accepted by this listener (a concurrent/re-entrant Dispatch offered
//     the same head and recorded first): skip, preserving at-most-once acceptance
//     per (event, listener) binding — the duplicate Offer is absorbed by the
//     idempotent-listener contract (INV-EVT-2).
//
// The store append stays under q.mu in phase 3 on purpose: the Store is not
// internally synchronized (its writes are serialized solely by q.mu), so moving
// it out would introduce a data race. Phase 3 is short and calls no listener
// code, unlike the original monolithic pass that held the lock across every
// Offer.
func (q *Queue) Dispatch() (accepted int) {
	// Phase 1 — SNAPSHOT (locked).
	q.mu.Lock()
	now := q.now()
	pending := make([]pendingOffer, 0, len(q.listeners))
	for _, ls := range q.listeners {
		if e := q.headFor(ls.l, now); e != nil {
			pending = append(pending, pendingOffer{ls: ls, evt: e.evt})
		}
	}
	q.mu.Unlock()

	// Phase 2 — OFFER (UNLOCKED). ls.l is set once at Register and never mutated,
	// so reading it here without the lock is safe.
	toRecord := make([]pendingOffer, 0, len(pending))
	for _, p := range pending {
		if p.ls.l.Offer(p.evt) {
			toRecord = append(toRecord, p)
		}
	}
	if len(toRecord) == 0 {
		return 0
	}

	// Phase 3 — RECORD (locked), re-validating each acceptance against current
	// state (see the locking-discipline note above).
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range toRecord {
		lid := p.ls.l.ID()
		e, ok := q.entries[p.evt.ID]
		if !ok {
			continue // entry left the queue mid-dispatch (expired/evicted): skip
		}
		if e.accepted[lid] {
			continue // already recorded by a concurrent/re-entrant pass: at-most-once
		}
		// In-memory accept first; the durable accept record is written AFTER
		// (ADR 0031 req 4) — the crash window that yields the at-least-once
		// redelivery (one extra re-offer per crash window).
		e.accepted[lid] = true
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
		q.maybeEvict(e)
	}
	return accepted
}

// maybeEvict evicts an event early when opted-in and every currently-bound
// listener has accepted it (ADR 0031). Caller holds q.mu.
func (q *Queue) maybeEvict(e *entry) {
	if !q.evictWhenAllAccept {
		return
	}
	for _, ls := range q.listeners {
		if ls.l.Matches(e.evt) && !e.accepted[ls.l.ID()] {
			return // a bound listener has not accepted yet
		}
	}
	_ = q.store.Append(Record{Op: opEvict, EventID: e.evt.ID})
	delete(q.entries, e.evt.ID)
	// Drop the evicted id from the FIFO spine too. Leaving it as a tombstone lets
	// a re-emit BEFORE the next Expire() append a SECOND spine entry (the stale-
	// removal branch in Enqueue only fires while q.entries still holds the id),
	// which double-counts the id (INV-OBS-1) and reorders delivery (ADR-0031
	// req 1). Bead pg2-f8btt.
	q.dropFromOrder(e.evt.ID)
}

// Expire drops every event past its ttl and returns how many were dropped. An
// event that expires with NO listener having accepted it is unconsumed-expired
// (INV-DISP-3 / INV-OBS-1) and is reported to the Observer. Retention is
// independent of consumer state: an event is dropped only at its ttl, never
// because a consumer is down or disabled.
func (q *Queue) Expire() (dropped int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	kept := q.order[:0:0]
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			continue // already evicted
		}
		if now.Before(e.deadline()) {
			kept = append(kept, id)
			continue
		}
		if len(e.accepted) == 0 {
			q.obs.OnUnconsumedExpired(e.evt.Type)
		}
		delete(q.entries, id)
		dropped++
	}
	q.order = kept
	return dropped
}

// Idle reports whether no (event, listener) pair is still deliverable — every
// enqueued event is accepted by each bound listener or has expired — the
// condition run-until-idle exits on (INV-LIFE-1). Caller must NOT hold q.mu.
func (q *Queue) Idle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for _, ls := range q.listeners {
		if q.headFor(ls.l, now) != nil {
			return false // a deliverable head is still outstanding (incl. fan-out)
		}
	}
	// A non-expired event that no one has accepted is still pending — an orphan
	// (no binding) or a disabled/absent consumer's event waiting to reach ttl. It
	// is neither accepted nor expired, so the queue is NOT drained (INV-LIFE-1).
	for _, id := range q.order {
		if e, ok := q.entries[id]; ok && now.Before(e.deadline()) && len(e.accepted) == 0 {
			return false
		}
	}
	return true
}

// DepthByType returns the per-type count of retained, non-expired events — the
// "queue depth" gauge source (INV-OBS-1). Caller must NOT hold q.mu.
func (q *Queue) DepthByType() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	depth := map[string]int{}
	for _, id := range q.order {
		if e, ok := q.entries[id]; ok && now.Before(e.deadline()) {
			depth[e.evt.Type]++
		}
	}
	return depth
}

// RunUntilIdle dispatches and expires on a fixed tick until the queue is idle
// (INV-LIFE-1) or ctx is cancelled. It is the drive loop behind the
// `run-until-idle` operator subcommand; a busy handler simply keeps its head
// re-offered until the head is accepted or its ttl expires.
//
// The between-pass wait blocks on the `after` seam (WithSleeper), NOT directly
// on time.After, so it advances on the SAME clock the ttl math uses. Under the
// real default this genuinely waits `tick`; under a mock clock whose `after`
// advances virtual time, the loop makes deadline progress and terminates on ttl
// deterministically without real sleeping (and without the frozen-clock
// busy-loop a real time.After caused when q.now never advanced).
func (q *Queue) RunUntilIdle(ctx context.Context, tick time.Duration) error {
	for {
		q.Expire()
		q.Dispatch()
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
