// Package tui implements pg-router's operator-facing terminal UI. This file
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
	Core  CoreInfo `json:"core"`
	Mode  string   `json:"mode"`
	Gates []Gate   `json:"gates"`
	// AsOf is when the core composed this reply; the gate list is read live at
	// that instant (the Gate Registry is a projection of the event log, not a
	// polled cache), so it doubles as the gates' freshness stamp.
	AsOf           time.Time `json:"asOf"`
	LastTickAt     time.Time `json:"lastTickAt"`
	SnapshotAt     time.Time `json:"snapshotAt"`
	TickIntervalMs int64     `json:"tickIntervalMs"`

	Queues     []Queue        `json:"queues"`
	Deliveries []Delivery     `json:"deliveries"`
	Registry   []Registration `json:"registry"`

	// Dispatch mirrors the wire's `dispatch` object (Task 6.5): the
	// real-time fan-out concurrency pair, additive to the frozen
	// status-field tree. A zero value (both fields 0) decodes an absent
	// `dispatch` object the same way it decodes one present with zero
	// counts -- composeStatusReply always sends it today, but nothing here
	// requires that.
	Dispatch Dispatch `json:"dispatch"`

	// QueueLog mirrors the wire's `queueLog` object (bead pg2-8e0m6): the
	// durable write-ahead log's size and compaction count. A zero value decodes
	// an absent object (an older core).
	QueueLog QueueLog `json:"queueLog"`

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

// Gate mirrors one entry of the wire's `gates` array: a Gate Registry gate
// in force (bead pg2-h63eu). The array lists ACTIVE gates only, so presence
// means "set"; an empty array means nothing is gated. Type is the arbitrary
// ALL-CAPS gate TYPE; Owner is DEBUG ONLY (shown, never acted on). Description,
// Owner, ExpiresAt and TTLRemainingMs are omitted on the wire when the gate
// carries none.
type Gate struct {
	Type           string    `json:"type"`
	Description    string    `json:"description"`
	Owner          string    `json:"owner"`
	SetAt          time.Time `json:"setAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	TTLRemainingMs int64     `json:"ttlRemainingMs"`
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

// Dispatch mirrors the wire's `dispatch` object (Task 6.5): Busy is
// Queue.SessionsInFlight() (len(custody), can exceed 1 post-Task 6.2's
// bounded fan-out) and Total is the active listener count
// (Queue.ListenerCount()) -- the busy/N dispatch-concurrency ratio, distinct
// from the pinned banner's own, unchanged "N in flight" wording (which
// reads Deliveries, above).
type Dispatch struct {
	Busy  int `json:"busy"`
	Total int `json:"total"`
}

// QueueLog mirrors the wire's `queueLog` object: the write-ahead log's size and
// compaction count (bead pg2-8e0m6) plus the log-size limit state (bead
// pg2-5d3ui). The limit fields are zero/empty against a core that predates them.
type QueueLog struct {
	Bytes          int64   `json:"bytes"`
	Compactions    int64   `json:"compactions"`
	LimitBytes     int64   `json:"limitBytes"`
	SoftBytes      int64   `json:"softBytes"`
	Percent        float64 `json:"percent"`
	State          string  `json:"state"`
	EmittersHalted bool    `json:"emittersHalted"`
	Rejected       struct {
		LogFull       int64 `json:"logFull"`
		LogUnwritable int64 `json:"logUnwritable"`
	} `json:"rejected"`
	Detail string `json:"detail"`
	// LastCompaction is the most recent compaction the core completed (bead
	// pg2-maxn1); nil before the first, or against a core that predates it.
	LastCompaction *LastCompaction `json:"lastCompaction"`
}

// LastCompaction mirrors the wire's `queueLog.lastCompaction` object.
type LastCompaction struct {
	At            time.Time `json:"at"`
	Trigger       string    `json:"trigger"`
	BytesBefore   int64     `json:"bytesBefore"`
	BytesAfter    int64     `json:"bytesAfter"`
	RecordsBefore int       `json:"recordsBefore"`
	RecordsAfter  int       `json:"recordsAfter"`
	DurationMs    int64     `json:"durationMs"`
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
	// Participant (DEC-OBS-2, bead pg2-ugcrb; INV-OBS-2) names which
	// configured listener or source this entry is about -- empty for an
	// entry no single participant settled (activity.Entry.Participant's own
	// doc). Drill-down's own per-participant history filter (drilldown.go)
	// reads this field.
	Participant string `json:"participant"`
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
	// Declined stays the existing flat total (backward compatible).
	Declined int64 `json:"declined"`
	// LastDeliveredAtMs is Unix millis of the most recent delivery (this
	// task); 0 means never delivered.
	LastDeliveredAtMs int64 `json:"lastDeliveredAtMs"`
	// DeclinedByReason decodes the wire's existing declinedByReason object
	// (this task is the first decoder of it) -- keys are DeclineReason's
	// own text or an arbitrary DeclineDetail override.
	DeclinedByReason map[string]int64 `json:"declinedByReason"`
	// HandlerFailures counts genuine business-logic rejections (this task)
	// -- NOT a decline, since the item was accepted; see
	// core.ListenerCounts.HandlerFailures' own doc.
	HandlerFailures int64    `json:"handlerFailures"`
	Backoff         *Backoff `json:"backoff"`
	// SelfReportState is this task's fold-in of the retired Registry pane:
	// the Registration.State whose ID equals this Listener's Role, or ""
	// if this listener has never self-reported.
	SelfReportState string `json:"selfReportState"`
	// InFlight/InFlightEventType (DEC-OBS-2, bead pg2-ugcrb; INV-OBS-2's
	// "processing now" signal) -- InFlight is true exactly while this role
	// has an outstanding offer; InFlightEventType is the offered event's
	// Type, empty when InFlight is false.
	InFlight          bool   `json:"inFlight"`
	InFlightEventType string `json:"inFlightEventType"`
	// Description is the operator-authored free-text note (pg2-ec754), or
	// "" if unset. Rendered as "-" by renderListenerDetail (drilldown.go),
	// matching this package's existing absent-value convention.
	Description string `json:"description"`
}

// DeclinedBucketed buckets DeclinedByReason into (busy, unavailable,
// other). Any key other than the two known DeclineReason strings --
// including a DeclineDetail override -- folds into other, so
// busy+unavailable+other always sums to len(DeclinedByReason)'s total,
// which always sums to Declined (core.go's own invariant, ListenerCounts'
// doc).
func (l Listener) DeclinedBucketed() (busy, unavailable, other int64) {
	for reason, n := range l.DeclinedByReason {
		switch reason {
		case "busy":
			busy += n
		case "unavailable":
			unavailable += n
		default:
			other += n
		}
	}
	return busy, unavailable, other
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
	// ExpectedIntervalMs is this source's own expected tick cadence in
	// milliseconds (this task). 0 means unknown.
	ExpectedIntervalMs int64 `json:"expectedIntervalMs"`
	// InFlight (DEC-OBS-2, bead pg2-ugcrb; INV-OBS-2's "processing now"
	// signal) is true exactly while this source has a fetch in progress.
	InFlight bool `json:"inFlight"`
	// Description is the operator-authored free-text note (pg2-ec754), or
	// "" if unset. Rendered as "-" by renderSourceDetail (drilldown.go),
	// matching this package's existing absent-value convention.
	Description string `json:"description"`
}
