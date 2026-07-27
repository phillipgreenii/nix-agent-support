package query

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

// EventQuery is the `event` query type spec C deliberately left unregistered and
// this design (M5) registers. It is an event SOURCE for the Aggregator / saga
// path: it emits one typed, correlated event carrying a work item, so a
// downstream role's opt-in Aggregator (Q2) can collect it by CorrelationID and
// fire when its Completeness condition is met.
//
// It is deliberately a producer under the current Env (no bus handle in the
// query Run seam). A bus-CONSUMING variant — a query that reads correlated
// events off the bus and emits an aggregate — needs an Env-bus seam and is a
// captured implementation-plan open item, not this first cut. The aggregation
// MECHANISM itself lives in the eventbus (Aggregator), which this type feeds.
type EventQuery struct {
	Meta          `toml:"-"`
	ItemID        string `toml:"item_id"`
	ItemType      string `toml:"item_type"`
	Title         string `toml:"title"`
	CorrelationID string `toml:"correlation_id"`
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

// Run emits a single correlated event of the query's primary emit type.
func (q EventQuery) Run(_ context.Context, _ Env) ([]event.Event, error) {
	e := event.NewItemEvent(firstEmit(q), "", item.Item{ID: q.ItemID, Type: q.ItemType, Title: q.Title})
	e.CorrelationID = q.CorrelationID
	return []event.Event{e}, nil
}
