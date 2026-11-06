package query

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/item"
)

// TimerQuery is the built-in TIMER EMITTER (`type = "timer"`): a source with no
// backing command that does nothing but emit a time-tick event of its declared
// type on its trigger's cadence — the trigger a "check something every five
// minutes" listener binds to.
//
// It is the one emitter NO gate ever blocks (Gate Registry, bead pg2-h63eu;
// IsTimer): "you can't stop time moving forward". That is deliberate and
// load-bearing — a watchdog listener driven by a timer must keep ticking while
// a gate is up, or it could never notice the condition clearing. The tick event
// is still subject to the ordinary gating of whoever consumes it: a listener
// blocked by an active gate is not dispatched to, and the tick it missed goes
// away at its final attempt (INV-EVT-4).
type TimerQuery struct {
	Meta `toml:"-"`
	// Metadata is static, operator-declared string metadata copied unchanged
	// into every tick's item. Nil/empty leaves the item exactly as it was before
	// the key existed.
	Metadata map[string]string `toml:"-"`
}

// Validate has nothing to check beyond the [[query]]-level `emits`, which the
// config registry already requires.
func (TimerQuery) Validate() error { return nil }

// BackingCommand is empty: a timer needs no executable.
func (TimerQuery) BackingCommand() string { return "" }

// IsTimer marks the emitter that no gate ever blocks.
func (TimerQuery) IsTimer() bool { return true }

// Slot is one calendar slot handed to a SlotRunner.
type Slot struct {
	// Name is the query's configured name; the calendar event id is derived from
	// it (dedup is global by event id, so two queries emitting one event type at
	// one slot must not share an id). Empty falls back to the emit type (the
	// smoke path, which has no name).
	Name string
	// Time is the slot instant.
	Time time.Time
	// OnMissed marks a catch-up tick (run-now after a missed slot).
	OnMissed bool
	// Now is the time of the evaluation that fires the slot.
	Now time.Time
}

// SlotRunner is the optional interface a query implements to be fired for a
// specific calendar slot; the driver type-switches on it when the query's
// trigger is a calendar trigger.
type SlotRunner interface {
	RunSlot(ctx context.Context, env Env, slot Slot) ([]event.Event, error)
}

// Run emits one tick event of the query's primary emit type. A period timer's
// id is unique per firing (the event is born expired, so its dedup window is
// zero anyway). For a calendar timer Run is the smoke shape: the same event
// RunSlot emits, for slot = now.
func (q TimerQuery) Run(ctx context.Context, env Env) ([]event.Event, error) {
	now := time.Now().UTC()
	if IsCalendar(q.Trigger()) {
		return q.RunSlot(ctx, env, Slot{Time: now, Now: now})
	}
	typ := firstEmit(q)
	if typ == "" {
		return nil, fmt.Errorf("timer query: no emit type declared")
	}
	it := item.Item{ID: now.Format(time.RFC3339), Type: "timer", Title: "timer tick"}
	it.Metadata = q.copyMetadata()
	return []event.Event{{
		ID:        fmt.Sprintf("timer:%s:%d", typ, uniqueNano(now)),
		Type:      typ,
		Item:      it,
		EmittedAt: now,
	}}, nil
}

var lastNano atomic.Int64

// uniqueNano returns now.UnixNano(), bumped by one when needed so two firings
// never share an id even on a host whose clock ticks coarser than a nanosecond.
func uniqueNano(now time.Time) int64 {
	n := now.UnixNano()
	for {
		prev := lastNano.Load()
		if n <= prev {
			n = prev + 1
		}
		if lastNano.CompareAndSwap(prev, n) {
			return n
		}
	}
}

// copyMetadata returns a fresh map per tick, never aliased to the query's own;
// nil when the query declares none.
func (q TimerQuery) copyMetadata() map[string]any {
	if len(q.Metadata) == 0 {
		return nil
	}
	m := make(map[string]any, len(q.Metadata))
	for k, v := range q.Metadata {
		m[k] = v
	}
	return m
}

// RunSlot emits the tick for one calendar slot:
//
//   - event id calendar:<query name>:<slot UTC RFC 3339>;
//   - Item.ID and metadata scheduled_for are the slot in the trigger zone's
//     offset, RFC 3339 at second precision (only the event id is UTC);
//   - metadata is the declared metadata plus scheduled_for, plus on_missed =
//     "true" on a catch-up tick only (an on-time tick OMITS the key, it does not
//     carry "false");
//   - Item.Type and Title keep the timer values;
//   - Attributes at and expiresAt are time.Time values in UTC (they are
//     RFC 3339 UTC when logged; discover.ToQueueEvent reads only time.Time, a
//     string would silently leave the event born expired). expiresAt is Next(slot),
//     or Now + expires_in when the query sets it.
func (q TimerQuery) RunSlot(_ context.Context, _ Env, slot Slot) ([]event.Event, error) {
	typ := firstEmit(q)
	if typ == "" {
		return nil, fmt.Errorf("timer query: no emit type declared")
	}
	ct, ok := Calendar(q.Trigger())
	if !ok {
		return nil, fmt.Errorf("timer query: RunSlot needs a calendar trigger")
	}
	name := slot.Name
	if name == "" {
		name = typ
	}
	st := slot.Time.Truncate(time.Second)
	local := st.In(ct.loc()).Format(time.RFC3339)
	md := q.copyMetadata()
	if md == nil {
		md = make(map[string]any, 2)
	}
	md["scheduled_for"] = local
	if slot.OnMissed {
		md["on_missed"] = "true"
	}
	expires := ct.Next(st)
	if ct.ExpiresIn > 0 {
		expires = slot.Now.Add(ct.ExpiresIn)
	}
	return []event.Event{{
		ID:   fmt.Sprintf("calendar:%s:%s", name, st.UTC().Format(time.RFC3339)),
		Type: typ,
		Item: item.Item{ID: local, Type: "timer", Title: "timer tick", Metadata: md},
		Attributes: map[string]any{
			"at":        st.UTC(),
			"expiresAt": expires.UTC(),
		},
		EmittedAt: slot.Now.UTC(),
	}}, nil
}

// IsTimer reports whether q is the timer emitter, which no gate blocks.
func IsTimer(q Query) bool {
	t, ok := q.(interface{ IsTimer() bool })
	return ok && t.IsTimer()
}
