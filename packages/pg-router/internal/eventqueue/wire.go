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
// `at`/`expiresAt`/`schemaVersion` carry `omitempty` because event.schema.json
// sets additionalProperties:false and types `at`/`expiresAt` as strings:
// EncodeEvent must OMIT an absent one rather than emit `""` / `null`, neither
// of which is what a source would have sent. `omitempty` affects marshalling
// only, so DecodeEvent is unchanged. id/type are deliberately NOT omitempty —
// they are the only required fields, and EncodeEvent's Validate guarantees
// them.
//
// `payload` is deliberately the ONE optional field WITHOUT `omitempty`
// (INTF-SOURCE, DEC-WIRE-1's payload normalization): the wire form always
// carries `payload` as a present JSON object, never omitted and never `null`,
// so a handler is never handed nothing in its place. EncodeEvent enforces this
// by never leaving Payload nil before marshalling (see its doc comment);
// without that, a nil map here would marshal as `null`, which is not an
// object and would violate event.schema.json's own typing of `payload`.
type wireEvent struct {
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	At            string         `json:"at,omitempty"`
	ExpiresAt     string         `json:"expiresAt,omitempty"`
	Payload       map[string]any `json:"payload"`
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
// An absent wire `payload` decodes to a non-nil, empty map — never nil
// (INTF-SOURCE, DEC-WIRE-1's payload normalization) — so every downstream
// reader (a binding's narrowing path, discover's ItemFromPayload) can index
// Payload unconditionally without a nil-map special case.
//
// DecodeEvent deliberately does NOT validate against the JSON Schema (that is
// package conformance's job, run at the boundary before decoding) nor against
// Event.Validate (Enqueue does that). The schema's own `pattern` on `at` /
// `expiresAt` (schemas/event.schema.json) already rejects a value that is not
// well-formed RFC3339 SHAPE, but a regex cannot enforce calendar semantics
// (e.g. a month of 13, a day of 45) — DecodeEvent's time.Parse call is still
// the authority that turns a syntactically-valid string into a time.Time and
// catches the values that slip past the pattern that way. It is ALSO the
// place that catches an unparseable instant for any caller reachable by
// DecodeEvent that skips schema validation first (belt-and-suspenders): it is
// the SINGLE wire→core decoder every ingest entry point shares — the
// `ingest-event` manager callback (internal/core) and the operator
// `push-inject` front door (internal/emit) — both of which currently DO run
// schema validation first, but nothing in this function's own contract
// depends on that. Either way, an unparseable `at`/`expiresAt` is classified
// as ErrInvalidEvent so a caller can report it as a malformed event.
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
	payload := w.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return Event{
		SchemaVersion: w.SchemaVersion,
		ID:            w.ID,
		Type:          w.Type,
		At:            at,
		ExpiresAt:     expiresAt,
		Payload:       payload,
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
//
// `payload`, unlike the instants, is ALWAYS emitted — present, an object — even
// when evt.Payload is nil or empty (INTF-SOURCE, DEC-WIRE-1's payload
// normalization): a handler is never handed nothing in its place. w.Payload is
// therefore never left nil before marshalling, so it renders as `{}` rather
// than `null`.
func EncodeEvent(evt Event) ([]byte, error) {
	if err := evt.Validate(); err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	w := wireEvent{
		SchemaVersion: evt.SchemaVersion,
		ID:            evt.ID,
		Type:          evt.Type,
		Payload:       evt.Payload,
	}
	if w.Payload == nil {
		w.Payload = map[string]any{}
	}
	if !evt.At.IsZero() {
		w.At = formatInstant(evt.At)
	}
	if !evt.ExpiresAt.IsZero() {
		w.ExpiresAt = formatInstant(evt.ExpiresAt)
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
