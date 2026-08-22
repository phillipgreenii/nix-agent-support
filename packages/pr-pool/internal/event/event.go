// Package event is pr-pool's leaf value type for the event model (design
// 2026-06-25-pr-pool-event-model-split-role-query-design). An Event is one
// typed, self-contained fact emitted by a query (or by another role's
// completion). Type is the topic roles bind to; Item is the work payload; the
// bus derives a dispatch context from an Event at delivery.
//
// This package is a leaf: it imports only internal/item (like internal/item
// itself imports nothing in-repo), keeping the import DAG acyclic. The design's
// intent is that events CROSS the bus while contexts are ephemeral and built at
// dispatch — so the transportable fact lives here.
package event

import (
	"time"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// ClockTick is the internal event type published once per drain tick (Q1: "the
// period tick is itself an event"). A PeriodTrigger query is, conceptually, a
// role-less consumer of this tick; the driver publishes it for uniformity and
// observability, then fires the period-driven queries.
const ClockTick = "clock.tick"

// Event is one typed, self-contained fact. Type is the topic roles subscribe to
// (Observer). Item generalizes the work payload every dispatch-triggering event
// carries. EmittedAt drives TTL (Q4) and ordering.
//
// Correlation/aggregation (the former CorrelationID field, event.CorrelationSpec,
// Completeness/AllOf/CountOf) was DELETED, not ported (bead pg2-f3mcb.2): the
// queue-as-universal-intermediary convergence has no aggregator, so there is
// nothing left to correlate events for.
type Event struct {
	ID         string         // unique per emission (dedup key, Q3)
	Type       string         // the topic — roles bind to this (Observer)
	Item       item.Item      // the work payload (id/type/title/metadata)
	Source     string         // emitting query name (provenance / observability)
	EmittedAt  time.Time      // for TTL (Q4) and ordering
	Attributes map[string]any // extra, type-specific fields for trigger strategies
}

// FingerprintID derives the stable dedup id for an item-carrying event
// (Q3). It is stable across passes while a bead remains ready, so a still-ready
// bead is not re-enqueued while its dispatch is in flight or leased. The design
// notes a claim-state component as a candidate refinement (deferred to the
// implementation plan's "Open items"); this first cut keys on (eventType,
// item.ID), which is sufficient for the in-pass dedup the built-ins need.
func FingerprintID(eventType, itemID string) string {
	return eventType + ":" + itemID
}

// NewItemEvent builds an item-carrying event of the given type, minting a stable
// FingerprintID and recording source provenance. EmittedAt is left zero for the
// bus to stamp at publish (so the TTL clock and the bus clock agree).
func NewItemEvent(eventType, source string, it item.Item) Event {
	return Event{
		ID:     FingerprintID(eventType, it.ID),
		Type:   eventType,
		Item:   it,
		Source: source,
	}
}

// Expired reports whether the event is past EmittedAt+ttl relative to now. A
// zero EmittedAt (never stamped) is treated as not expired — the bus stamps it
// at publish, so an unstamped event has not yet entered the TTL window.
func (e Event) Expired(now time.Time, ttl time.Duration) bool {
	if e.EmittedAt.IsZero() || ttl <= 0 {
		return false
	}
	return now.After(e.EmittedAt.Add(ttl))
}
