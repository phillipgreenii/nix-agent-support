package eventqueue

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"time"
)

// This file is the Gate Registry (bead pg2-h63eu): the generic, TYPE-keyed
// gate mechanism that replaced pg-router's three hard-coded file-backed gates
// (operator-paused, cicd-down, disk-space-low).
//
// A gate is what PREVENTS pg-router FROM ROUTING. It is
//
//   - a TYPE (an ALL-CAPS string; arbitrary — nothing in this package knows any
//     particular TYPE except by the SYSTEM_PAUSE constant the operator pause
//     verbs map onto),
//   - an optional description,
//   - an optional owner (the identity of whoever set it — DEBUG ONLY: it has no
//     behavioral effect and is overwritten on re-set), and
//   - an optional TTL lease.
//
// There is at most one active gate per TYPE, last writer wins, and ANY caller
// may clear any gate.
//
// STORAGE. Gates are records in the SAME write-ahead log events use, appended
// through the SAME Store interface (Store.Append / Store.AppendBatch), under
// the same q.mu: GateSet (each lease renewal is another GateSet carrying the
// new expiry), GateCleared and GateExpired. The active-gate set (Queue.gates)
// is a PROJECTION of that log — the latest record per TYPE, with a lapsed TTL
// counting as inactive — rebuilt by replay, so gates persist across restarts. A
// gate's state does NOT depend on any queued event: it is a log record, not an
// entry the queue's retention sweep can evict, so an event drop can never
// un-set a gate.
//
// ROUTING. Each gate record is ALSO routable like an ordinary event: when some
// registered listener binds one of the GateEvent* types, the record is enqueued
// as an event of that type too (payload: type/description/owner/setAt/
// expiresAt), so a participant may subscribe to gate changes. Gate events
// ALWAYS bypass gating for delivery — otherwise a participant that blocks on
// TYPE X could never learn X was cleared. (An agent-added rule the operator did
// not object to; confirm if revisited, per the bead.)
//
// EFFECT. A gate acts on EMITTERS and LISTENERS, NEVER on events. While any
// gate is active, per participant that blocks on it: a blocked emitter is not
// polled; a blocked listener is not dispatched to and its pull is rejected
// naming the gate; pushed events are still ACCEPTED, acks and confirmations
// still processed, queued events stay queued, and an UNBLOCKED listener still
// receives them. Each participant declares at registration which gate TYPEs it
// does NOT block on (default: blocks on every TYPE). The timer emitter is never
// blocked by any gate (see query.Timer).
//
// EVENT TTL. When an event is about to expire, every listener that has not yet
// received it gets one final attempt (INV-EVT-4). A listener that is still
// gate-blocked at that point loses the event ("the event goes away for it"):
// long gates therefore cause event drops, and the queue is bounded by event
// TTL. Each such drop is counted (GateObserver.OnGateDrop).

// GateEvent types are the routable event types the Gate Registry's records
// carry. They are an ordinary, bindable event type vocabulary: a role binds
// "gate.set" to hear about a gate being set or renewed.
const (
	GateEventSet     = "gate.set"
	GateEventCleared = "gate.cleared"
	GateEventExpired = "gate.expired"
)

// IsGateEvent reports whether typ is one of the Gate Registry's routable
// record types — the events that bypass gating for delivery.
func IsGateEvent(typ string) bool {
	switch typ {
	case GateEventSet, GateEventCleared, GateEventExpired:
		return true
	}
	return false
}

// GateSystemPause is the gate TYPE `pg-router pause` sets and `resume` clears
// (formerly the operator_paused file-backed gate).
const GateSystemPause = "SYSTEM_PAUSE"

// ErrInvalidGate is returned for a structurally invalid gate request.
var ErrInvalidGate = errors.New("invalid gate")

// gateTypeRE is the shape of a gate TYPE: ALL CAPS (letters, digits and
// underscore, starting with a letter). The TYPE vocabulary is arbitrary, but
// its SHAPE is fixed so a TYPE is safe as a metric label, a CLI argument and a
// log field.
var gateTypeRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ValidateGateType reports whether t is an acceptable gate TYPE.
func ValidateGateType(t string) error {
	if !gateTypeRE.MatchString(t) {
		return fmt.Errorf("%w: type %q must be ALL CAPS (A-Z, 0-9, _; starting with a letter; at most 64 characters)", ErrInvalidGate, t)
	}
	return nil
}

// Gate is one active (or lapsed-but-unswept) gate: the projection of the
// latest GateSet record for its TYPE.
type Gate struct {
	Type        string
	Description string
	// Owner is the identity of whoever set the gate. DEBUG ONLY: nothing
	// branches on it, and a later set overwrites it.
	Owner string
	// SetAt is when the latest GateSet record was written (a lease renewal
	// moves it forward).
	SetAt time.Time
	// ExpiresAt is the end of the TTL lease; zero means no lease (the gate
	// lasts until cleared).
	ExpiresAt time.Time
}

// ActiveAt reports whether g is in force at now: a gate with no lease always
// is; a leased one is until its lease lapses.
func (g Gate) ActiveAt(now time.Time) bool {
	return g.ExpiresAt.IsZero() || now.Before(g.ExpiresAt)
}

// TTLRemaining returns the time left on g's lease at now, and whether g has a
// lease at all.
func (g Gate) TTLRemaining(now time.Time) (time.Duration, bool) {
	if g.ExpiresAt.IsZero() {
		return 0, false
	}
	d := g.ExpiresAt.Sub(now)
	if d < 0 {
		d = 0
	}
	return d, true
}

// GateRequest is a caller's request to set (or renew) a gate.
type GateRequest struct {
	Type        string
	Description string
	Owner       string
	// TTL is the lease length; zero or negative means no lease.
	TTL time.Duration
}

// GateObserver receives the Gate Registry's lifecycle and effect signals for
// metric emission. It is separate from Observer (queue.go) so existing
// Observer implementations need no change; install one with WithGateObserver.
// Every hook fires AFTER q.mu is released, like Observer's.
type GateObserver interface {
	// OnGateSet fires per GateSet record; renewal is true when it replaced an
	// already-active gate of the same TYPE (a lease renewal or overwrite).
	OnGateSet(gateType string, renewal bool)
	// OnGateCleared fires when a clear removed an active gate; held is how long
	// the (latest) set had been in force.
	OnGateCleared(gateType string, held time.Duration)
	// OnGateExpired fires when the sweep retired a lapsed lease; held is how
	// long the (latest) set had been in force.
	OnGateExpired(gateType string, held time.Duration)
	// OnGateBlocked fires each time a gate stops a participant from doing work
	// it had: kind is "dispatch" (a blocked listener with a deliverable head
	// was skipped), "poll" (a blocked emitter was not polled) or "pull" (a
	// blocked listener's pull was rejected). participant is the listener or
	// source name; gateType is the blocking gate.
	OnGateBlocked(participant, kind, gateType string)
	// OnGateDrop fires when an event goes away for a gate-blocked listener at
	// its final attempt (INV-EVT-4): evtType is the dropped event's type.
	OnGateDrop(evtType, listenerID, gateType string)
}

type noopGateObserver struct{}

func (noopGateObserver) OnGateSet(string, bool)               {}
func (noopGateObserver) OnGateCleared(string, time.Duration)  {}
func (noopGateObserver) OnGateExpired(string, time.Duration)  {}
func (noopGateObserver) OnGateBlocked(string, string, string) {}
func (noopGateObserver) OnGateDrop(string, string, string)    {}

// WithGateObserver installs a GateObserver.
func WithGateObserver(o GateObserver) Option {
	return func(q *Queue) { q.gateObs = o }
}

// GateExempter is implemented by a Listener that declared, at registration,
// the gate TYPEs it does NOT block on. A Listener without it blocks on every
// TYPE (the default).
type GateExempter interface {
	NonBlockingGates() []string
}

// GateBlockedError is returned when a gate stops a participant's request (a
// listener's pull): it names the gate responsible.
type GateBlockedError struct {
	Participant string
	Gate        string
}

func (e *GateBlockedError) Error() string {
	return fmt.Sprintf("participant %q is blocked by gate %s", e.Participant, e.Gate)
}

// ErrGateBlocked lets a caller test errors.Is(err, ErrGateBlocked) without
// naming *GateBlockedError.
var ErrGateBlocked = errors.New("blocked by gate")

// Is makes *GateBlockedError match ErrGateBlocked.
func (e *GateBlockedError) Is(target error) bool { return target == ErrGateBlocked }

// BlockingGate returns the first (by TYPE, so the answer is deterministic)
// gate in active that a participant with the given exemptions blocks on, and
// whether there is one. A nil/empty exempt set blocks on every gate.
func BlockingGate(active []Gate, exempt map[string]bool) (string, bool) {
	for _, g := range active {
		if !exempt[g.Type] {
			return g.Type, true
		}
	}
	return "", false
}

// ExemptSet builds the lookup BlockingGate takes from a registration's
// non-blocking TYPE list.
func ExemptSet(types []string) map[string]bool {
	if len(types) == 0 {
		return nil
	}
	m := make(map[string]bool, len(types))
	for _, t := range types {
		m[t] = true
	}
	return m
}

// gatePayload renders g as the routable event's payload.
func gatePayload(g Gate, by string) map[string]any {
	p := map[string]any{"type": g.Type}
	if g.Description != "" {
		p["description"] = g.Description
	}
	if g.Owner != "" {
		p["owner"] = g.Owner
	}
	if !g.SetAt.IsZero() {
		p["setAt"] = g.SetAt.UTC().Format(time.RFC3339Nano)
	}
	if !g.ExpiresAt.IsZero() {
		p["expiresAt"] = g.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if by != "" {
		p["by"] = by
	}
	return p
}

// gateFromRecord rebuilds the projection entry a GateSet record carries.
func gateFromRecord(r Record) Gate {
	return Gate{Type: r.GateType, Description: r.Description, Owner: r.Owner, SetAt: r.At, ExpiresAt: r.ExpiresAt}
}

// activeGatesLocked returns the gates in force at now, sorted by TYPE.
// Caller holds q.mu.
func (q *Queue) activeGatesLocked(now time.Time) []Gate {
	if len(q.gates) == 0 {
		return nil
	}
	out := make([]Gate, 0, len(q.gates))
	for _, g := range q.gates {
		if g.ActiveAt(now) {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// ActiveGates returns the gates in force right now (a lapsed lease is
// inactive), sorted by TYPE. Caller must NOT hold q.mu.
func (q *Queue) ActiveGates() []Gate {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeGatesLocked(q.now())
}

// Gate returns the active gate of the given TYPE, if there is one.
func (q *Queue) Gate(gateType string) (Gate, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	g, ok := q.gates[gateType]
	if !ok || !g.ActiveAt(q.now()) {
		return Gate{}, false
	}
	return g, true
}

// anyListenerMatchesLocked reports whether some registered listener binds evt —
// the condition for also routing a gate record as an event. Caller holds q.mu.
func (q *Queue) anyListenerMatchesLocked(evt Event) bool {
	for _, ls := range q.listeners {
		if ls.l.Matches(evt) {
			return true
		}
	}
	return false
}

// gateEventLocked builds the routable event for a gate record, or false when no
// registered listener would receive it (nothing to route, so nothing is
// enqueued — gate records themselves are always logged). Caller holds q.mu.
func (q *Queue) gateEventLocked(typ string, g Gate, by string, now time.Time) (Event, bool) {
	id, err := newDispatchID()
	if err != nil {
		return Event{}, false
	}
	evt := Event{
		ID:      "gate-" + id,
		Type:    typ,
		At:      now,
		Payload: gatePayload(g, by),
	}.Resolve(now)
	if !q.anyListenerMatchesLocked(evt) {
		return Event{}, false
	}
	return evt, true
}

// addEventLocked applies an enqueue to the in-memory queue exactly as Enqueue's
// fresh-id branch does, after the caller has made the enqueue Record durable.
// Caller holds q.mu.
func (q *Queue) addEventLocked(evt Event) {
	q.entries[evt.ID] = newEntry(evt)
	q.order = append(q.order, evt.ID)
	q.publishCellLocked("", evt.Type, evt.Type)
}

// SetGate sets (or renews, or overwrites) the gate of req.Type: last writer
// wins, and the new GateSet record carries the new description, owner and
// lease. The returned Gate is the projection after the call.
func (q *Queue) SetGate(req GateRequest) (Gate, error) {
	if err := ValidateGateType(req.Type); err != nil {
		return Gate{}, err
	}
	q.mu.Lock()
	unlock := q.unlockOnce()
	defer unlock()
	now := q.now()
	g := Gate{Type: req.Type, Description: req.Description, Owner: req.Owner, SetAt: now}
	if req.TTL > 0 {
		g.ExpiresAt = now.Add(req.TTL)
	}
	prev, had := q.gates[req.Type]
	renewal := had && prev.ActiveAt(now)
	recs := []Record{{Op: opGateSet, GateType: g.Type, Description: g.Description, Owner: g.Owner, At: g.SetAt, ExpiresAt: g.ExpiresAt}}
	evt, routed := q.gateEventLocked(GateEventSet, g, "", now)
	if routed {
		recs = append(recs, recordFromEvent(evt, now))
	}
	if err := q.store.AppendBatch(recs); err != nil {
		return Gate{}, err
	}
	q.gates[g.Type] = g
	if routed {
		q.addEventLocked(evt)
	}
	unlock()
	q.gateObs.OnGateSet(g.Type, renewal)
	if routed {
		q.obs.OnEnqueue(evt)
	}
	return g, nil
}

// ClearGate clears the active gate of the given TYPE; by identifies the caller
// (logged and routed, never enforced — ANY caller may clear any gate). It
// reports whether there was an active gate to clear; clearing a TYPE with no
// active gate is a no-op success that writes nothing.
func (q *Queue) ClearGate(gateType, by string) (Gate, bool, error) {
	if err := ValidateGateType(gateType); err != nil {
		return Gate{}, false, err
	}
	q.mu.Lock()
	unlock := q.unlockOnce()
	defer unlock()
	now := q.now()
	g, ok := q.gates[gateType]
	if !ok || !g.ActiveAt(now) {
		return Gate{}, false, nil
	}
	recs := []Record{{Op: opGateCleared, GateType: g.Type, Owner: by, At: now}}
	evt, routed := q.gateEventLocked(GateEventCleared, g, by, now)
	if routed {
		recs = append(recs, recordFromEvent(evt, now))
	}
	if err := q.store.AppendBatch(recs); err != nil {
		return Gate{}, false, err
	}
	delete(q.gates, gateType)
	if routed {
		q.addEventLocked(evt)
	}
	unlock()
	q.gateObs.OnGateCleared(g.Type, now.Sub(g.SetAt))
	if routed {
		q.obs.OnEnqueue(evt)
	}
	return g, true, nil
}

// expireGatesLocked retires every gate whose lease has lapsed at now: it appends
// one GateExpired record per gate (plus its routed event, when something would
// receive it) and removes it from the projection. It returns the expired gates
// and the routed events (for the caller's post-unlock hooks). A store failure
// is logged and leaves the gates in place, to be retried on the next sweep —
// they are already INACTIVE by ActiveAt, so a failed write cannot make an
// expired lease linger. Caller holds q.mu.
func (q *Queue) expireGatesLocked(now time.Time) (expired []Gate, events []Event) {
	var lapsed []Gate
	for _, g := range q.gates {
		if !g.ActiveAt(now) {
			lapsed = append(lapsed, g)
		}
	}
	if len(lapsed) == 0 {
		return nil, nil
	}
	sort.Slice(lapsed, func(i, j int) bool { return lapsed[i].Type < lapsed[j].Type })
	var recs []Record
	var evts []Event
	for _, g := range lapsed {
		recs = append(recs, Record{Op: opGateExpired, GateType: g.Type, At: now})
		if evt, ok := q.gateEventLocked(GateEventExpired, g, "", now); ok {
			recs = append(recs, recordFromEvent(evt, now))
			evts = append(evts, evt)
		}
	}
	if err := q.store.AppendBatch(recs); err != nil {
		logGateAppendFailure(len(lapsed), err)
		return nil, nil
	}
	for _, g := range lapsed {
		delete(q.gates, g.Type)
	}
	for _, evt := range evts {
		q.addEventLocked(evt)
	}
	return lapsed, evts
}

// CheckPull is the admission check for a listener's PULL request: it returns a
// *GateBlockedError naming the gate when listenerID is blocked by an active
// gate, nil otherwise. exempt is the TYPE set the listener declared at
// registration (ExemptSet). A rejection is counted via OnGateBlocked.
func (q *Queue) CheckPull(listenerID string, exempt map[string]bool) error {
	return q.checkBlocked(listenerID, "pull", exempt)
}

// EmitterBlockedBy reports whether an active gate stops the named emitter from
// being polled, and which. A blocked poll is counted via OnGateBlocked. The
// caller is responsible for never asking about the timer emitter, which no gate
// blocks.
func (q *Queue) EmitterBlockedBy(emitterID string, exempt map[string]bool) (string, bool) {
	q.mu.Lock()
	active := q.activeGatesLocked(q.now())
	q.mu.Unlock()
	t, blocked := BlockingGate(active, exempt)
	if blocked {
		q.gateObs.OnGateBlocked(emitterID, "poll", t)
	}
	return t, blocked
}

func (q *Queue) checkBlocked(participant, kind string, exempt map[string]bool) error {
	q.mu.Lock()
	active := q.activeGatesLocked(q.now())
	q.mu.Unlock()
	t, blocked := BlockingGate(active, exempt)
	if !blocked {
		return nil
	}
	q.gateObs.OnGateBlocked(participant, kind, t)
	return &GateBlockedError{Participant: participant, Gate: t}
}

// gateDrop is one event lost to a gate-blocked listener at its final attempt.
type gateDrop struct {
	evtType, listener, gate string
}

// gateBlock is one blocked dispatch observed while snapshotting a pass.
type gateBlock struct {
	listener, gate string
}

// gateSignals collects the observer notifications snapshotPending's locked
// phase produces, to be fired after q.mu is released.
type gateSignals struct {
	drops  []gateDrop
	blocks []gateBlock
}

func (q *Queue) fireGateSignals(s gateSignals) {
	for _, b := range s.blocks {
		q.gateObs.OnGateBlocked(b.listener, "dispatch", b.gate)
	}
	for _, d := range s.drops {
		q.gateObs.OnGateDrop(d.evtType, d.listener, d.gate)
	}
}

// dropGateBlockedLocked settles, for the gate-blocked listener ls, every event it
// is owed a FINAL attempt on (the event is already expired — INV-EVT-4) but
// cannot be offered because a gate blocks it: the event "goes away for it".
// Gate events are never dropped this way (they bypass gating). Events not yet
// expired are untouched — they stay queued. Returns whether ls still has a
// deliverable, non-gate head (a blocked DISPATCH, for the metric). Caller holds
// q.mu.
func (q *Queue) dropGateBlockedLocked(ls *listenerState, gateType string, now time.Time, sig *gateSignals) (blockedWork bool) {
	lid := ls.l.ID()
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok || IsGateEvent(e.evt.Type) || e.settled[lid] || !ls.l.Matches(e.evt) {
			continue
		}
		if e.evt.Expired(now) {
			e.settled[lid] = true
			if ls.declineEventID == e.evt.ID {
				ls.resetBackoff()
			}
			sig.drops = append(sig.drops, gateDrop{evtType: e.evt.Type, listener: lid, gate: gateType})
			continue
		}
		blockedWork = true
	}
	return blockedWork
}

// headForGateEvents is headFor restricted to the Gate Registry's routable
// events — the only events a gate-blocked listener may still be offered.
// Caller holds q.mu.
func (q *Queue) headForGateEvents(l Listener) *entry {
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok || !IsGateEvent(e.evt.Type) {
			continue
		}
		if e.settled[l.ID()] || !l.Matches(e.evt) {
			continue
		}
		return e
	}
	return nil
}

// logGateAppendFailure records a swallowed gate_expired write. The lapsed
// leases are already inactive (Gate.ActiveAt), so nothing is lost by retrying
// at the next sweep.
func logGateAppendFailure(n int, err error) {
	slog.Error("eventqueue: gate_expired append failed; the lapsed gate(s) are already inactive and will be retired by the next sweep",
		"count", n, "err", err)
}
