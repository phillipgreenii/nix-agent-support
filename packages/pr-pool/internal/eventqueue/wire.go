package eventqueue

import (
	"encoding/json"
	"fmt"
	"time"
)

// wireEvent is the ON-THE-WIRE event shape (schemas/event.schema.json): `ttl` is
// a duration string like "15m" and `at` an RFC3339 timestamp string, whereas the
// in-core Event carries a time.Duration and a time.Time.
type wireEvent struct {
	SchemaVersion string         `json:"schemaVersion"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	TTL           string         `json:"ttl"`
	At            string         `json:"at"`
	Payload       map[string]any `json:"payload"`
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
