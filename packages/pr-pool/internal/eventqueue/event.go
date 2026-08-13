// Package eventqueue implements pr-pool's durable, ordered, de-duped,
// retention-bounded event queue (ADR 0031, INV-EVT-1/2/3/4, INV-CONC-1,
// INV-FAIL-1, INV-LIFE-1). It realizes the OBSERVABLE behavior the behavior docs
// describe; the storage MECHANISM is an injectable Store (a JSONL write-ahead log
// ships as the default), kept a realization choice per ADR 0031.
//
// Expiry is an ABSOLUTE INSTANT, not a duration: an event carries an optional
// `at` and an optional `expiresAt`, both of which DEFAULT (see Event.Resolve),
// and the bound is evaluated AT ATTEMPT TIME (see Queue.Dispatch). That shape is
// decided in `packages/pr-pool/docs/decisions · DEC-EVENT-1`, which amends
// ADR 0031's duration-valued bound in part; the queue decision itself stands.
//
// This is bead pg2-hvlyj.16 (plan item 5.4). The queue's per-package statement
// coverage is gated at >=90% by the `pr-pool-go-tests` flake check (bead
// pg2-hvlyj.19).
package eventqueue

import (
	"errors"
	"fmt"
	"time"
)

// Event is a typed event flowing from a source, through the queue, to a
// handler. It mirrors the INTF-SOURCE event shape in the behavior docs
// (interfaces.md): id, type, at, expiresAt, payload.
type Event struct {
	// SchemaVersion is the message-schema envelope version (interfaces.md
	// "common manager contract"). Optional on the in-core representation; the
	// wire schemas (bead .13) enforce it on the boundary.
	SchemaVersion string
	// ID uniquely identifies the event. The core de-duplicates on it across the
	// retained id set, which lives exactly as long as the event does (INV-EVT-3).
	ID string
	// Type is the primary field a binding matches on (INV-DISP-1).
	Type string
	// At is the event's source-stamped creation time. OPTIONAL: zero means the
	// core's own now at ingest is used (INV-EVT-1, resolved by Resolve).
	At time.Time
	// ExpiresAt is the ABSOLUTE INSTANT past which an attempt is the last one a
	// handler is owed (INV-EVT-4). OPTIONAL: zero means At is used (INV-EVT-1,
	// resolved by Resolve). Nothing computes a duration, and neither field is
	// configured — they ride on the event (DEC-EVENT-1).
	ExpiresAt time.Time
	// Payload MUST be a JSON object (keyed structure), OPAQUE to the core unless
	// a binding declares a matchable path (INV-DISP-1). The core neither reads
	// nor validates it beyond "is an object".
	Payload map[string]any
}

// ErrInvalidEvent is returned by Validate for a structurally invalid event.
var ErrInvalidEvent = errors.New("invalid event")

// Validate enforces the structural rules the core relies on before an event
// enters the queue: id and type are required, and payload (if present) must be an
// object. Malformed events are rejected at the ingest boundary (interfaces.md
// ingest-event `rejected` list); this is that check in core-facing form.
//
// Neither instant is validated, and that is deliberate rather than an omission.
// `at` and `expiresAt` are both OPTIONAL and both DEFAULT (Resolve), so their
// absence is the normal case, and an `expiresAt` ALREADY IN THE PAST MUST NOT be
// rejected either: the DEFAULT event is born expired (`expiresAt` resolves to
// `at`, which resolves to the core's ingest-now), so a past instant is the
// contract's own default behavior — offer once to every matching handler, then
// drop (INV-EVT-4, DEC-EVENT-1). Rejecting it would reject nearly every event.
func (e Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: missing required field %q", ErrInvalidEvent, "id")
	}
	if e.Type == "" {
		return fmt.Errorf("%w: missing required field %q", ErrInvalidEvent, "type")
	}
	// Payload is optional, but when present must be a keyed structure. A nil map
	// is treated as "absent" (allowed); the wire layer distinguishes null from a
	// non-object via the JSON Schema.
	return nil
}

// Resolve returns the event with both optional instants filled in against the
// core's `now` at ingest, which is the ONE place the INV-EVT-1 defaults are
// applied: an absent `at` becomes now, and an absent `expiresAt` becomes the
// resolved `at`.
//
// Two consequences follow from the defaults and are INTENDED (DEC-EVENT-1):
//
//   - The DEFAULT event (neither field set) resolves to expiresAt == at == now,
//     so it is BORN EXPIRED. Its first attempt is therefore also its last, and
//     the default delivery behavior is "offer once to every matching handler,
//     then drop" — a best-effort default needing no configuration.
//   - `expiresAt` IS the retry window (INV-EVT-4) and the de-duplication window
//     (INV-EVT-3) at once, because retention runs to the same instant. Setting it
//     in the future is how retries are requested; there is no second knob.
//
// The queue stores the RESOLVED event, so a handler is offered concrete instants
// and a replay reconstructs the same bound without re-deriving it from a clock
// that has since moved.
func (e Event) Resolve(now time.Time) Event {
	if e.At.IsZero() {
		e.At = now
	}
	if e.ExpiresAt.IsZero() {
		e.ExpiresAt = e.At
	}
	return e
}

// Expired reports whether the event is already past its expiry at instant now —
// the STATELESS check INV-EVT-4 evaluates at attempt time. It MUST be called on a
// RESOLVED event (Resolve); on an unresolved one a zero ExpiresAt would read as
// long expired rather than as "defaults to at".
//
// The comparison is the whole decision: the core keeps NO attempt history, and
// the delivery-opportunity guarantee still holds because a born-expired event's
// first attempt is also its last (INV-EVT-1, DEC-EVENT-1).
func (e Event) Expired(now time.Time) bool { return !now.Before(e.ExpiresAt) }
