package eventqueue

import (
	"encoding/json"
	"fmt"
	"time"
)

// wireEvent is the ON-THE-WIRE event shape (schemas/event.schema.json): `at` and
// `expiresAt` are RFC3339 timestamp STRINGS, whereas the in-core Event carries
// time.Time values.
//
// The encoding is the implementation's own choice (DEC-EVENT-1 "Not decided
// here"), settled by matching what this wire protocol already does: RFC3339
// strings under camelCase keys, exactly as the INTF-SOURCE event shape in
// interfaces.md illustrates them. `INV-INTF-2`'s conformance suite is where this
// side and the doc side reconcile.
//
// The OPTIONAL fields carry `omitempty` because event.schema.json sets
// additionalProperties:false and types `at`/`expiresAt` as strings and `payload`
// as an object: EncodeEvent must OMIT an absent field rather than emit `""` /
// `null`, neither of which is what a source would have sent. `omitempty` affects
// marshalling only, so DecodeEvent is unchanged. id/type are deliberately NOT
// omitempty — they are the only required fields, and EncodeEvent's Validate
// guarantees them.
type wireEvent struct {
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	At            string         `json:"at,omitempty"`
	ExpiresAt     string         `json:"expiresAt,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// DecodeEvent decodes one wire event (event.schema.json shape) into the in-core
// Event, converting the optional `at` and `expiresAt` timestamps.
//
// It is the SINGLE wire→core event decoder every ingest entry point shares — the
// `ingest-event` manager callback (internal/core) and the operator `push-inject`
// front door (internal/emit) — so the two can never disagree about how a wire
// event becomes an Event.
//
// It decodes only; it does NOT apply the INV-EVT-1 defaults. Both instants stay
// ZERO when absent, and Event.Resolve fills them in at INGEST, against the core's
// own clock — the only clock entitled to define "now" for this event. Resolving
// here would bind the defaults to whichever process happened to parse the bytes,
// which for a forwarded push-inject is the operator's CLI, not the core.
//
// DecodeEvent deliberately does NOT validate against the JSON Schema (that is
// package conformance's job, run at the boundary before decoding) nor against
// Event.Validate (Enqueue does that). It reports only the conversions a
// structural schema cannot express — an unparseable `at` or `expiresAt`, both
// just strings to the schema — classified as ErrInvalidEvent so a caller can
// report them as a malformed event.
func DecodeEvent(data []byte) (Event, error) {
	var w wireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	at, err := parseInstant("at", w.At)
	if err != nil {
		return Event{}, err
	}
	expiresAt, err := parseInstant("expiresAt", w.ExpiresAt)
	if err != nil {
		return Event{}, err
	}
	return Event{
		SchemaVersion: w.SchemaVersion,
		ID:            w.ID,
		Type:          w.Type,
		At:            at,
		ExpiresAt:     expiresAt,
		Payload:       w.Payload,
	}, nil
}

// EncodeEvent encodes an in-core Event back to the ON-THE-WIRE event shape — the
// exact inverse of DecodeEvent, and the SINGLE wire encoder every forwarding
// entry point shares.
//
// It exists because the operator `push-inject` front door (internal/emit) has
// already DECODED the operator's event by the time it discovers that the core
// lives in another process: forwarding it over the socket means re-encoding, and
// hand-rolling that shape a second time is exactly how a forwarded event drifts
// out of what the receiving core's DecodeEvent accepts. One encoder + one decoder
// pinned to each other by a round-trip test cannot drift.
//
// Validate runs FIRST: an Event missing an id/type has no valid wire form, and
// encoding it anyway would push the failure onto the receiving core as an opaque
// "malformed" rejection instead of reporting it where the fault is. An UNSET
// instant is NOT a fault — it is the documented default (Event.Resolve) — so it
// is omitted and the receiving core resolves it against its own clock.
func EncodeEvent(evt Event) ([]byte, error) {
	if err := evt.Validate(); err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	w := wireEvent{
		SchemaVersion: evt.SchemaVersion,
		ID:            evt.ID,
		Type:          evt.Type,
	}
	if !evt.At.IsZero() {
		w.At = formatInstant(evt.At)
	}
	if !evt.ExpiresAt.IsZero() {
		w.ExpiresAt = formatInstant(evt.ExpiresAt)
	}
	if len(evt.Payload) > 0 {
		w.Payload = evt.Payload
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encode event %s: %w", evt.ID, err)
	}
	return data, nil
}

// parseInstant reads one optional wire instant (`at` / `expiresAt`, RFC3339 per
// event.schema.json). An ABSENT (empty) value yields the zero time, which
// Event.Resolve later reads as "apply the INV-EVT-1 default"; a
// present-but-unparseable value is a malformed event.
func parseInstant(field, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: unparseable %s %q: %v", ErrInvalidEvent, field, s, err)
	}
	return t, nil
}

// formatInstant renders one wire instant. RFC3339Nano (not RFC3339) so a
// core-resolved instant — which comes from a real clock and carries sub-second
// precision — survives an encode→decode round trip unchanged; it renders a
// whole-second time identically to RFC3339, so the shape interfaces.md
// illustrates is unaffected. UTC so the wire form is comparable byte-for-byte
// regardless of the sender's zone.
func formatInstant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
