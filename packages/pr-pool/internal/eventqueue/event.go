// Package eventqueue implements pr-pool's durable, ordered, de-duped,
// TTL-bounded event queue (ADR 0031, INV-EVT-1/2/3, INV-CONC-1, INV-FAIL-1,
// INV-LIFE-1). It realizes the OBSERVABLE behavior the behavior docs describe;
// the storage MECHANISM is an injectable Store (a JSONL write-ahead log ships
// as the default), kept a realization choice per ADR 0031.
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
// (interfaces.md): id, type, ttl, at, payload.
type Event struct {
	// SchemaVersion is the message-schema envelope version (interfaces.md
	// "common manager contract"). Optional on the in-core representation; the
	// wire schemas (bead .13) enforce it on the boundary.
	SchemaVersion string
	// ID uniquely identifies the event. The core de-duplicates on it across the
	// retained-until-ttl id set (INV-EVT-3).
	ID string
	// Type is the primary field a binding matches on (INV-DISP-1).
	Type string
	// TTL is how long the core holds, offers, and retains the event before
	// dropping it if still unaccepted (INV-EVT-1). Parsed from a duration string
	// like "15m" on the wire.
	TTL time.Duration
	// At is the event's source-stamped creation time. MAY be zero, in which case
	// the core's own now at ingest applies (INV-EVT-1). The behavior contract now
	// bounds expiry by an absolute instant rather than a duration, so there is no
	// clock origin left to choose; this implementation still derives expiry from
	// ingest time and TTL until the wire contract catches up (see Queue).
	At time.Time
	// Payload MUST be a JSON object (keyed structure), OPAQUE to the core unless
	// a binding declares a matchable path (INV-DISP-1). The core neither reads
	// nor validates it beyond "is an object".
	Payload map[string]any
}

// ErrInvalidEvent is returned by Validate for a structurally invalid event.
var ErrInvalidEvent = errors.New("invalid event")

// Validate enforces the structural rules the core relies on before an event
// enters the queue: id, type, and a positive ttl are required, and payload (if
// present) must be an object. Malformed events are rejected at the ingest
// boundary (interfaces.md ingest-event `rejected` list); this is that check in
// core-facing form.
func (e Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: missing required field %q", ErrInvalidEvent, "id")
	}
	if e.Type == "" {
		return fmt.Errorf("%w: missing required field %q", ErrInvalidEvent, "type")
	}
	if e.TTL <= 0 {
		return fmt.Errorf("%w: ttl must be a positive duration (got %s)", ErrInvalidEvent, e.TTL)
	}
	// Payload is optional, but when present must be a keyed structure. A nil map
	// is treated as "absent" (allowed); the wire layer distinguishes null from a
	// non-object via the JSON Schema.
	return nil
}

// ParseTTL parses a duration string like "15m" / "90s" / "1h30m" into a
// time.Duration, rejecting empty or non-positive values. It is the core's
// reader for the wire `ttl` string.
func ParseTTL(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("%w: empty ttl", ErrInvalidEvent)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%w: unparseable ttl %q: %v", ErrInvalidEvent, s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%w: non-positive ttl %q", ErrInvalidEvent, s)
	}
	return d, nil
}
