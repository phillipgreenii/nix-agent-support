// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.4) carries only the wire-decode target; the Poller that fills it
// in lives in poller.go.
package tui

import "time"

// StatusReply is the typed decode target for the cli.status-reply wire
// shape (schemas/cli.status-reply.schema.json), as WIDENED by Task 4.1
// (operator-widened scope): Listeners []Listener and Sources []Source carry
// the new per-role/per-source fields; every other field here decodes an
// EXISTING (pre-widening) top-level property composeStatusReply already
// emits (internal/core/core.go) — this type does not invent any new wire
// shape, it only names Go types for what the wire already sends.
//
// Deliberately NOT decoded here (out of this packet's scope, per the
// docket's "Flagged for operator" extension note): `resolvedConfig` and
// `counters` — Task 4.1's own operator-widened additions, left as a
// pre-existing gap for a later packet. Also not decoded: the wire's
// `schemaVersion` (protocol plumbing, never operator-visible state) and the
// legacy `config` object (sources/handlers counts) — nothing in this phase's
// TUI screens renders either.
type StatusReply struct {
	Core            CoreInfo  `json:"core"`
	Mode            string    `json:"mode"`
	Gates           []Gate    `json:"gates"`
	GatesObservedAt time.Time `json:"gatesObservedAt"`
	AsOf            time.Time `json:"asOf"`
	LastTickAt      time.Time `json:"lastTickAt"`
	SnapshotAt      time.Time `json:"snapshotAt"`
	TickIntervalMs  int64     `json:"tickIntervalMs"`

	Queues     []Queue        `json:"queues"`
	Deliveries []Delivery     `json:"deliveries"`
	Registry   []Registration `json:"registry"`

	UnmatchedBindings []string        `json:"unmatchedBindings"`
	Activity          []ActivityEntry `json:"activity"`
	ActivityDropped   bool            `json:"activityDropped"`

	// Listeners / Sources are Task 4.1's own per-role / per-source WIDENED
	// views (operator-widened scope): role/binds/enabled/excluded/
	// delivered/declined/backoff, and name/type/enabled/excluded/mode/
	// lastTick/failure respectively.
	Listeners []Listener `json:"listeners"`
	Sources   []Source   `json:"sources"`
}

// CoreInfo mirrors the wire's `core` object: the running core's identity and
// lifecycle state. Every field is optional on the wire (composeStatusReply
// always sends the object, but StartedAt/Version can be empty pre-first-
// tick); a zero Go value decodes an absent JSON field the same way it
// decodes an empty string, so nothing here needs a pointer.
type CoreInfo struct {
	State      string    `json:"state"`
	Version    string    `json:"version"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"startedAt"`
	ConfigPath string    `json:"configPath"`
}

// Gate mirrors one entry of the wire's `gates` array (INV-LIFE-2's two named
// gates). Mtime/Owner are omitted on the wire when the gate carries neither
// (composeStatusReply's statusGates), decoding to their zero values here.
type Gate struct {
	Name  string    `json:"name"`
	Set   bool      `json:"set"`
	Mtime time.Time `json:"mtime"`
	Owner string    `json:"owner"`
}

// Queue mirrors one entry of the wire's `queues` array: a per-type depth.
type Queue struct {
	Type  string `json:"type"`
	Depth int    `json:"depth"`
}

// Delivery mirrors one entry of the wire's `deliveries` array (currently
// always empty — handleStatus's own doc: no tracking-id source this docket
// phase — decoded here anyway so a future non-empty reply needs no schema
// change on this side).
type Delivery struct {
	ID      string `json:"id"`
	Handler string `json:"handler"`
	Event   string `json:"event"`
}

// Registration mirrors one entry of the wire's `registry` array: a
// self-reported participant, distinct from (and unfiltered by role/kind,
// unlike) Listener.
type Registration struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Self  string `json:"self"`
}

// ActivityEntry mirrors one entry of the wire's `activity` array (the
// activity ring's own Read order: oldest first).
type ActivityEntry struct {
	Seq       uint64    `json:"seq"`
	StartedAt time.Time `json:"startedAt"`
	Type      string    `json:"type"`
	Outcome   string    `json:"outcome"`
}

// Backoff mirrors a Listener's `backoff` object (nil/null when the listener
// is not currently backing off).
type Backoff struct {
	Streak       int       `json:"streak"`
	NextEligible time.Time `json:"nextEligible"`
}

// Failure mirrors a Source's `failure` object (nil/null when the source has
// no recorded failure).
type Failure struct {
	Count        int       `json:"count"`
	NextEligible time.Time `json:"nextEligible"`
}

// Listener mirrors one entry of the WIDENED `listeners` array (Task 4.1,
// operator-widened scope): the full configured role set, including a
// selector-excluded role.
type Listener struct {
	Role      string   `json:"role"`
	Binds     []string `json:"binds"`
	Enabled   bool     `json:"enabled"`
	Excluded  bool     `json:"excluded"`
	Delivered int64    `json:"delivered"`
	Declined  int64    `json:"declined"`
	Backoff   *Backoff `json:"backoff"`
}

// Source mirrors one entry of the WIDENED `sources` array (Task 4.1): the
// full configured source set, including a selector-excluded source.
type Source struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Enabled  bool      `json:"enabled"`
	Excluded bool      `json:"excluded"`
	Mode     string    `json:"mode"`
	LastTick time.Time `json:"lastTick"`
	Failure  *Failure  `json:"failure"`
}
