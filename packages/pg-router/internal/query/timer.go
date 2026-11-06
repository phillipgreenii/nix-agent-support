package query

import (
	"context"
	"fmt"
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

// Run emits one tick event of the query's primary emit type. The id is unique
// per firing (the event is born expired, so its dedup window is zero anyway).
func (q TimerQuery) Run(_ context.Context, _ Env) ([]event.Event, error) {
	typ := firstEmit(q)
	if typ == "" {
		return nil, fmt.Errorf("timer query: no emit type declared")
	}
	now := time.Now().UTC()
	it := item.Item{ID: now.Format(time.RFC3339), Type: "timer", Title: "timer tick"}
	if len(q.Metadata) > 0 {
		// A fresh map per tick, never aliased to the query's own.
		it.Metadata = make(map[string]any, len(q.Metadata))
		for k, v := range q.Metadata {
			it.Metadata[k] = v
		}
	}
	return []event.Event{{
		ID:        fmt.Sprintf("timer:%s:%d", typ, now.UnixNano()),
		Type:      typ,
		Item:      it,
		EmittedAt: now,
	}}, nil
}

// IsTimer reports whether q is the timer emitter, which no gate blocks.
func IsTimer(q Query) bool {
	t, ok := q.(interface{ IsTimer() bool })
	return ok && t.IsTimer()
}
