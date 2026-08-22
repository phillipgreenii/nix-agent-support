package query

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

// EventQuery is the `event` query type spec C deliberately left unregistered
// and this design (M5) registers. It is a plain event source: it emits one
// typed event carrying a work item.
//
// Correlation/aggregation support (a CorrelationID field, an Aggregator to feed)
// was DELETED, not ported (bead pg2-f3mcb.2): the queue-as-universal-
// intermediary convergence has no aggregator. This query type's own removal is
// tracked separately (pg2-9d0he).
type EventQuery struct {
	Meta     `toml:"-"`
	ItemID   string `toml:"item_id"`
	ItemType string `toml:"item_type"`
	Title    string `toml:"title"`
}

// Validate requires an item id (the work payload) and at least one emit type
// (roles bind to it). Meta is installed before Validate by the factory.
func (q EventQuery) Validate() error {
	if q.ItemID == "" {
		return fmt.Errorf("event query: item_id is required")
	}
	if firstEmit(q) == "" {
		return fmt.Errorf("event query: emits is required (the event type to publish)")
	}
	return nil
}

// BackingCommand is "": an event source produces its event in-process and shells
// out to nothing.
func (q EventQuery) BackingCommand() string { return "" }

// Run emits a single event of the query's primary emit type.
func (q EventQuery) Run(_ context.Context, _ Env) ([]event.Event, error) {
	e := event.NewItemEvent(firstEmit(q), "", item.Item{ID: q.ItemID, Type: q.ItemType, Title: q.Title})
	return []event.Event{e}, nil
}
