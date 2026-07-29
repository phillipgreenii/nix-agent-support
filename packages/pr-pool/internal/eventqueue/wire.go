package eventqueue

import (
	"encoding/json"
	"fmt"
	"time"
)

// wireEvent is the ON-THE-WIRE event shape (schemas/event.schema.json): `ttl` is
// a duration string like "15m" and `at` an RFC3339 timestamp string, whereas the
// in-core Event carries a time.Duration and a time.Time.
//
// The OPTIONAL fields carry `omitempty` because event.schema.json sets
// additionalProperties:false and types `at` as a string and `payload` as an
// object: EncodeEvent must OMIT an absent `at`/`payload` rather than emit `""` /
// `null`, neither of which is what a source would have sent. `omitempty` affects
// marshalling only, so DecodeEvent is unchanged. id/type/ttl are deliberately
// NOT omitempty — they are required, and EncodeEvent's Validate guarantees them.
type wireEvent struct {
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	TTL           string         `json:"ttl"`
	At            string         `json:"at,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// DecodeEvent decodes one wire event (event.schema.json shape) into the in-core
// Event, converting the `ttl` duration string and the optional `at` timestamp.
//
// It is the SINGLE wire→core event decoder every ingest entry point shares — the
// `ingest-event` manager callback (internal/core) and the operator `push-inject`
// front door (internal/emit) — so the two can never disagree about how a wire
// event becomes an Event. It lives beside ParseTTL, the core's reader for the
// wire `ttl` string.
//
// DecodeEvent deliberately does NOT validate against the JSON Schema (that is
// package conformance's job, run at the boundary before decoding) nor against
// Event.Validate (Enqueue does that). It reports only the conversions a
// structural schema cannot express: an unparseable/non-positive `ttl` and an
// unparseable `at`, both classified as ErrInvalidEvent so a caller can report
// them as a malformed event.
func DecodeEvent(data []byte) (Event, error) {
	var w wireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	ttl, err := ParseTTL(w.TTL)
	if err != nil {
		return Event{}, err
	}
	at, err := parseAt(w.At)
	if err != nil {
		return Event{}, err
	}
	return Event{
		SchemaVersion: w.SchemaVersion,
		ID:            w.ID,
		Type:          w.Type,
		TTL:           ttl,
		At:            at,
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
// Validate runs FIRST: an Event missing an id/type or carrying a non-positive ttl
// has no valid wire form, and encoding it anyway would push the failure onto the
// receiving core as an opaque "malformed" rejection instead of reporting it where
// the fault is.
func EncodeEvent(evt Event) ([]byte, error) {
	if err := evt.Validate(); err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	w := wireEvent{
		SchemaVersion: evt.SchemaVersion,
		ID:            evt.ID,
		Type:          evt.Type,
		// Duration.String() renders "15m" as "15m0s"; time.ParseDuration (and so
		// ParseTTL) reads it back to the same duration, which is what the round-trip
		// contract requires. The wire `ttl` is a duration STRING, not a pinned format.
		TTL: evt.TTL.String(),
	}
	if !evt.At.IsZero() {
		w.At = evt.At.UTC().Format(time.RFC3339)
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

// parseAt reads the optional wire `at` source-stamp (RFC3339, event.schema.json).
// `at` MAY be absent (empty), in which case Event.At is the zero time — the core
// treats At as optional (OQ-EVT-TTL-ORIGIN: this implementation's TTL clock
// origin is ingest time, not At); a present-but-unparseable value is a malformed
// event, classified like ParseTTL's rejection.
func parseAt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: unparseable at %q: %v", ErrInvalidEvent, s, err)
	}
	return t, nil
}
