// Package query is pr-pool's typed-union of work SOURCES (producers). Each
// concrete query emits []event.Event; bead-backed queries map beads.Issue ->
// item.Item -> Event. Run errors are propagated, never returned as "no work"
// (pg2-qq9v): a bd/exec failure must not masquerade as an idle pool.
//
// Under the event model (design 2026-06-25) a query is a PRODUCER: Run returns
// typed events (was []item.Item), the query declares the event type(s) it Emits
// (roles bind to these), and a pluggable Trigger strategy decides when it fires.
// Emits/Trigger are carried by an embedded Meta so every concrete type gets them
// uniformly; the type-specific fields still decode from the query sub-table.
package query

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

type QueryFormat string

const (
	FormatJSONL QueryFormat = "jsonl"
	FormatJSON  QueryFormat = "json"
)

// Commander runs an executable and returns its stdout (one-method interface, like
// beads.Runner / ccpool.Runner — not a bare func field).
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// Env carries the capabilities a query needs. The orchestrator builds it from its
// own fields in phase 1 (the Deps bag arrives in phase 2).
type Env struct {
	BD       beads.Runner
	RepoRoot string
	Cmd      Commander
}

// Query is a producer of typed events. The critical inversion (design M2): Run
// returns []event.Event (was []item.Item), and the query declares the event
// type(s) it Emits plus its firing Trigger. A role and a query are wired ONLY
// through a shared event-type string (the producer/consumer decoupling).
type Query interface {
	Validate() error
	// Emits returns the event type(s) this query produces (for wiring +
	// orphan-emit validation).
	Emits() []string
	// Trigger is the query's firing strategy (Q1) — Strategy pattern. A nil
	// return is treated as PeriodTrigger by the driver.
	Trigger() Trigger
	// FailureBackoff is this query's pull-source failure backoff (INV-FAIL-3,
	// pg2-0c8yz): the retry cadence discover.Produce consults when Run fails,
	// distinct from Trigger's success-path interval. The zero value (Retries: 0)
	// means fail fast — exactly the original pg2-qq9v behavior — so a query that
	// has not opted in is unaffected.
	FailureBackoff() FailureBackoff
	// BackingCommand returns the executable this source needs in order to run,
	// or "" when it needs none (a pure in-process producer). It is a STATIC
	// declaration, resolved pre-runtime by config.Validate's absent-backing-command
	// check, so every concrete query states it explicitly rather than inheriting a
	// silent default — a new query type that shells out and forgets it would
	// otherwise escape that check.
	BackingCommand() string
	// Run produces zero or more events for the bus to publish.
	Run(ctx context.Context, env Env) ([]event.Event, error)
}

// Meta carries the event-model wiring common to every concrete query: the event
// type(s) it emits and its firing trigger. It is embedded (value) so a query
// gets Emits()/Trigger() for free; config sets it from the [[query]]-level
// emits/trigger keys (NOT the type-specific sub-table), and the built-in Go
// query set constructs it inline.
type Meta struct {
	EmitTypes []string
	Trig      Trigger
	// FB is this query's pull-source failure backoff (INV-FAIL-3). The zero
	// value (Retries: 0) reproduces today's fail-fast behavior exactly, so an
	// unconfigured query is unaffected — opting in is [query.failure_backoff] or
	// the pool-level default (config.Registry.buildQuery).
	FB FailureBackoff
}

// Emits returns the configured emit type(s).
func (m Meta) Emits() []string { return m.EmitTypes }

// Trigger returns the configured trigger, defaulting to PeriodTrigger{} (fire on
// every tick) when unset so an unconfigured query reproduces today's behavior.
func (m Meta) Trigger() Trigger {
	if m.Trig == nil {
		return PeriodTrigger{}
	}
	return m.Trig
}

// FailureBackoff returns the configured pull-source failure backoff (INV-FAIL-3).
func (m Meta) FailureBackoff() FailureBackoff { return m.FB }

// setMeta lets the factory/config install the [[query]]-level wiring onto a
// concrete query decoded from its sub-table. A pointer to an embedded Meta
// satisfies this, so *BeadsReady et al. get it via promotion.
func (m *Meta) setMeta(x Meta) { *m = x }

// metaSetter is the seam the factory uses to install Meta post-decode.
type metaSetter interface{ setMeta(Meta) }

// Source is a named producer: a query with the config name it was registered
// under (the [[query]].name). The name is provenance (Event.Source) and the
// handle run-query resolves. Emits/Trigger live on the embedded Query.
type Source struct {
	Name  string
	Query Query
}

// SourceSet is the ordered set of producers a drain fires (config order).
type SourceSet []Source

// FromIssue maps a single bd issue to an item, copying its metadata (keeps item a
// leaf — the adapter lives here). The query/drain path (fromIssues) and the
// direct-bead run-role path (pg2-jpci) share this one adapter, so a dispatched Item
// carries the bead's metadata identically no matter which path built it.
func FromIssue(i beads.Issue) item.Item {
	return item.Item{ID: i.ID, Type: i.Type, Title: i.Title, Metadata: i.Metadata}
}

// eventsFromIssues maps bd issues to events of the given type via FromIssue. It
// is the shared M2 wrapper: each item.Item becomes an Event{Type, Item} with a
// stable FingerprintID (Q3 dedup). source is the emitting query name.
func eventsFromIssues(in []beads.Issue, eventType, source string) []event.Event {
	out := make([]event.Event, 0, len(in))
	for _, i := range in {
		out = append(out, event.NewItemEvent(eventType, source, FromIssue(i)))
	}
	return out
}

// eventsFromItems wraps already-built items into events of the given type
// (command / issue queries build items from their own source).
func eventsFromItems(in []item.Item, eventType, source string) []event.Event {
	out := make([]event.Event, 0, len(in))
	for _, it := range in {
		out = append(out, event.NewItemEvent(eventType, source, it))
	}
	return out
}

// firstEmit returns the query's primary emit type, or "" if it declares none.
// The built-in single-emit queries wrap every item under this type.
func firstEmit(q Query) string {
	e := q.Emits()
	if len(e) == 0 {
		return ""
	}
	return e[0]
}

// IsStub reports whether a query type is a not-yet-implemented stub. No query
// types are stubs currently; the seam is retained so the drain pre-flight can
// keep warning if a future type lands as a decode/validate-only stub.
func IsStub(Query) bool { return false }
