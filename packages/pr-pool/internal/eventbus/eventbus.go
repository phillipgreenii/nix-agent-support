// Package eventbus is pr-pool's in-pass Message Bus / Broker (design
// 2026-06-25-pr-pool-event-model-split-role-query-design). It decouples query
// PRODUCERS from role CONSUMERS: queries Publish typed events; roles Subscribe
// to event types (Observer) and Lease events off their own per-role queue
// (Competing Consumers). The bus owns routing (Q3 per-role topology), TTL
// eviction (Q4 lazy sweep), capacity-aware delivery (Q5), and the opt-in
// Aggregator (Q2).
//
// Scope (design "What we deliberately do NOT adopt"): the bus lives for ONE
// drain pass — it is in-process, not a long-lived daemon, and does NOT persist
// the queue. Durability across passes is the bead store's job (an expired event
// is re-emitted by re-running the query, "re-emission, not resurrection"). This
// is a deliberately different, lighter altitude than internal/eventqueue's
// durable JSONL-backed queue (ADR 0031), which serves a separate feature.
package eventbus

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
)

// Logger is the observability sink the bus logs every transition to (publish,
// lease, ack/nack, expire, aggregate-complete, aggregate-expire). It is
// structurally satisfied by *eventlog.Writer, so the bus stays decoupled from
// the concrete writer and unit-testable with a fake. A nil Logger is a no-op.
type Logger interface {
	Emit(level, kind, msg string, fields map[string]any) error
}

// Bus is the broker. It is safe for concurrent use, though the single-shot
// DrainOnce model drives it serially.
type Bus struct {
	mu  sync.Mutex
	now func() time.Time
	ttl time.Duration // default event TTL (Q4) — derived from MaxWait
	log Logger

	subs   map[string][]string   // eventType -> subscribed role names (Observer)
	queues map[string]*roleQueue // roleName -> its queue (Q3/Q5)
	aggs   map[string]*aggregate // roleName -> opt-in aggregator (Q2)
}

// roleQueue is one role-name's queue: a FIFO of held events with a leased set.
// Leased events stay HELD (in events) until Ack, so a still-ready re-publish is
// deduped by id while its dispatch is in flight (Q3).
type roleQueue struct {
	events map[string]event.Event // all held events (available + leased), by id
	order  []string               // FIFO of held event ids
	leased map[string]bool        // ids currently leased (inflight, Q5)
}

func newRoleQueue() *roleQueue {
	return &roleQueue{events: map[string]event.Event{}, leased: map[string]bool{}}
}

// Option configures a Bus.
type Option func(*Bus)

// WithClock injects the clock seam (default time.Now) for deterministic TTL
// tests.
func WithClock(now func() time.Time) Option { return func(b *Bus) { b.now = now } }

// WithTTL sets the default event TTL (Q4). Design default: MaxWait.
func WithTTL(ttl time.Duration) Option { return func(b *Bus) { b.ttl = ttl } }

// WithLogger installs the observability sink (Logger). Nil is a no-op.
func WithLogger(l Logger) Option { return func(b *Bus) { b.log = l } }

// New constructs a Bus. A fresh Bus is built per drain pass.
func New(opts ...Option) *Bus {
	b := &Bus{
		now:    time.Now,
		subs:   map[string][]string{},
		queues: map[string]*roleQueue{},
		aggs:   map[string]*aggregate{},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Subscribe registers a role name as a consumer of an event type (Observer).
// Idempotent per (roleType, eventType).
func (b *Bus) Subscribe(roleName, eventType string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.subs[eventType] {
		if r == roleName {
			return
		}
	}
	b.subs[eventType] = append(b.subs[eventType], roleName)
	if b.queues[roleName] == nil {
		b.queues[roleName] = newRoleQueue()
	}
}

// SubscribeAggregate registers a role name's OPT-IN Aggregator over the given
// bound event types (Q2). Correlated events of those types are held in a
// pending-aggregate keyed by CorrelationID until the spec's Completeness is met,
// then ONE aggregated event is enqueued to the role's queue. Subscribing an
// aggregator also subscribes the role to each bound type.
func (b *Bus) SubscribeAggregate(roleName string, binds []string, spec event.CorrelationSpec) {
	b.mu.Lock()
	if b.queues[roleName] == nil {
		b.queues[roleName] = newRoleQueue()
	}
	b.aggs[roleName] = &aggregate{
		spec:      spec,
		matches:   toSet(binds),
		pending:   map[string][]event.Event{},
		firstSeen: map[string]time.Time{},
	}
	b.mu.Unlock()
	for _, t := range binds {
		b.Subscribe(roleName, t)
	}
}

// Publish enqueues an event onto every subscribed role's queue (Observer
// fan-out, Q3). A role with an Aggregator (Q2) receives correlated events into
// its pending-aggregate instead of directly onto its queue. TTL-expired events
// are swept first (lazy sweep, Q4). Dedup: a re-publish of an id still held by a
// role's queue is dropped for that role (Q3).
func (b *Bus) Publish(_ context.Context, e event.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if e.EmittedAt.IsZero() {
		e.EmittedAt = now // stamp so the TTL clock and the bus clock agree
	}
	b.sweepLocked(now)
	b.emit("info", "event_published", "event published", map[string]any{
		"event": e.ID, "type": e.Type, "source": e.Source, "correlation": e.CorrelationID,
	})
	for _, roleName := range b.subs[e.Type] {
		if agg := b.aggs[roleName]; agg != nil && e.CorrelationID != "" {
			b.aggregateLocked(roleName, agg, e, now)
			continue
		}
		b.enqueueLocked(roleName, e)
	}
	return nil
}

// enqueueLocked adds e to roleName's queue unless already held (dedup by id).
func (b *Bus) enqueueLocked(roleName string, e event.Event) {
	q := b.queues[roleName]
	if q == nil {
		q = newRoleQueue()
		b.queues[roleName] = q
	}
	if _, held := q.events[e.ID]; held {
		return // dedup: still held (available or leased) — do not re-enqueue (Q3)
	}
	q.events[e.ID] = e
	q.order = append(q.order, e.ID)
}

// Lease pulls up to n ready events for roleName, respecting TTL (Q4). Returned
// events are marked leased (Competing Consumers: one event to one consumer) and
// stay held until Ack/Nack. n<=0 returns none — the caller passes n = Cap −
// Inflight (Q5), so a role at cap leases nothing.
func (b *Bus) Lease(_ context.Context, roleName string, n int) ([]event.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.sweepLocked(now)
	q := b.queues[roleName]
	if q == nil || n <= 0 {
		return nil, nil
	}
	var out []event.Event
	for _, id := range q.order {
		if len(out) >= n {
			break
		}
		if q.leased[id] {
			continue
		}
		e, ok := q.events[id]
		if !ok {
			continue
		}
		q.leased[id] = true
		out = append(out, e)
		b.emit("info", "event_leased", "event leased", map[string]any{"event": id, "role": roleName})
	}
	return out, nil
}

// Ack permanently removes a leased event from roleName's queue (consumed).
func (b *Bus) Ack(_ context.Context, roleName, eventID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[roleName]
	if q == nil {
		return nil
	}
	delete(q.events, eventID)
	delete(q.leased, eventID)
	q.dropFromOrder(eventID)
	b.emit("info", "event_acked", "event acked", map[string]any{"event": eventID, "role": roleName})
	return nil
}

// Nack un-leases an event so it returns to roleName's available queue (re-offer
// on a later lease). It does NOT drop the event — TTL (Q4) still bounds it.
func (b *Bus) Nack(_ context.Context, roleName, eventID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[roleName]
	if q == nil {
		return nil
	}
	delete(q.leased, eventID)
	b.emit("info", "event_nacked", "event nacked", map[string]any{"event": eventID, "role": roleName})
	return nil
}

// Inflight is the count of leased-but-not-Acked events for roleName (Q5). The
// capacity gate is n = Cap − Inflight.
func (b *Bus) Inflight(roleName string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[roleName]
	if q == nil {
		return 0
	}
	return len(q.leased)
}

// Depth returns the number of currently-held (non-expired) events of eventType
// across all role queues, deduped by event id. It is the ThresholdTrigger
// ("enough-events", Q1) input.
func (b *Bus) Depth(eventType string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(b.now())
	seen := map[string]bool{}
	for _, q := range b.queues {
		for id, e := range q.events {
			if e.Type == eventType {
				seen[id] = true
			}
		}
	}
	return len(seen)
}

// sweepLocked evicts every TTL-expired event across all queues and every expired
// incomplete aggregate (Q4 lazy sweep). Expiry is logged so silent loss is
// visible. Caller holds b.mu.
func (b *Bus) sweepLocked(now time.Time) {
	if b.ttl <= 0 {
		return
	}
	// role names sorted for deterministic log ordering
	names := make([]string, 0, len(b.queues))
	for name := range b.queues {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		q := b.queues[name]
		kept := q.order[:0:0]
		for _, id := range q.order {
			e, ok := q.events[id]
			if !ok {
				continue
			}
			if e.Expired(now, b.ttl) {
				delete(q.events, id)
				delete(q.leased, id)
				b.emit("warn", "event_expired", "event expired (TTL); re-emission on next tick if still ready", map[string]any{
					"event": id, "type": e.Type, "role": name,
				})
				continue
			}
			kept = append(kept, id)
		}
		q.order = kept
	}
	b.sweepAggregatesLocked(now)
}

func (b *Bus) emit(level, kind, msg string, fields map[string]any) {
	if b.log == nil {
		return
	}
	_ = b.log.Emit(level, kind, msg, fields)
}

func (q *roleQueue) dropFromOrder(id string) {
	kept := q.order[:0:0]
	for _, x := range q.order {
		if x != id {
			kept = append(kept, x)
		}
	}
	q.order = kept
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
