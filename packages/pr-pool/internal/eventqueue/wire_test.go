package eventqueue

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// DecodeEvent converts the wire shape (RFC3339 `at` / `expiresAt`) into the
// in-core Event.
func TestDecodeEvent_FullEvent(t *testing.T) {
	raw := `{"schemaVersion":"1","id":"evt-1","type":"review-requested","at":"2026-07-16T12:00:00Z","expiresAt":"2026-07-16T12:15:00Z","payload":{"pr":42}}`
	got, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if got.SchemaVersion != "1" || got.ID != "evt-1" || got.Type != "review-requested" {
		t.Fatalf("envelope fields = %+v", got)
	}
	wantAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if !got.At.Equal(wantAt) {
		t.Fatalf("at = %s, want %s", got.At, wantAt)
	}
	wantExpiry := time.Date(2026, 7, 16, 12, 15, 0, 0, time.UTC)
	if !got.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expiresAt = %s, want %s", got.ExpiresAt, wantExpiry)
	}
	if got.Payload["pr"] != float64(42) {
		t.Fatalf("payload = %v, want the opaque payload carried through", got.Payload)
	}
}

// Both instants are OPTIONAL, and DecodeEvent does NOT apply their defaults: it
// leaves them ZERO for Event.Resolve to fill in at INGEST, against the CORE's
// clock. Resolving at decode time would bind the defaults to whichever process
// parsed the bytes — for a forwarded push-inject, the operator's CLI.
func TestDecodeEvent_InstantsAreOptionalAndUnresolved(t *testing.T) {
	got, err := DecodeEvent([]byte(`{"id":"e","type":"t"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if !got.At.IsZero() {
		t.Fatalf("at = %s, want the zero time when absent (the core resolves it)", got.At)
	}
	if !got.ExpiresAt.IsZero() {
		t.Fatalf("expiresAt = %s, want the zero time when absent (the core resolves it)", got.ExpiresAt)
	}
}

// An `at` with no `expiresAt` decodes as-is; the "expiresAt defaults to at" rule
// belongs to Resolve, not the decoder.
func TestDecodeEvent_AtWithoutExpiresAt(t *testing.T) {
	got, err := DecodeEvent([]byte(`{"id":"e","type":"t","at":"2026-07-16T12:00:00Z"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if got.At.IsZero() || !got.ExpiresAt.IsZero() {
		t.Fatalf("at=%s expiresAt=%s, want a decoded at and an untouched zero expiresAt", got.At, got.ExpiresAt)
	}
}

// DecodeEvent is exercised directly here, bypassing the schema layer — so this
// proves DecodeEvent itself still rejects an unparseable `at`/`expiresAt` as
// ErrInvalidEvent even for a caller that skips schema validation. (Through the
// full pipeline, event.schema.json's `pattern` on these fields would already
// reject most of these values — a malformed shape, not merely "just strings to
// the schema" as before pg2-kgydy; only a calendar-invalid-but-shape-valid
// value, e.g. a month of 13, would reach this same check unaided.)
func TestDecodeEvent_Rejections(t *testing.T) {
	cases := map[string]string{
		"unparseable at":            `{"id":"e","type":"t","at":"yesterday"}`,
		"unparseable expiresAt":     `{"id":"e","type":"t","expiresAt":"soon"}`,
		"duration-shaped expiresAt": `{"id":"e","type":"t","expiresAt":"15m"}`,
		"non-RFC3339 at":            `{"id":"e","type":"t","at":"2026-07-16 12:00:00"}`,
	}
	for desc, raw := range cases {
		t.Run(desc, func(t *testing.T) {
			if _, err := DecodeEvent([]byte(raw)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("err = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

// The rejection names WHICH instant was unparseable, so an operator who typed one
// of two similar fields wrong is told which.
func TestDecodeEvent_RejectionNamesTheField(t *testing.T) {
	_, err := DecodeEvent([]byte(`{"id":"e","type":"t","expiresAt":"soon"}`))
	if err == nil || !strings.Contains(err.Error(), "expiresAt") {
		t.Fatalf("err = %v, want the offending field named", err)
	}
}

func TestDecodeEvent_MalformedJSON(t *testing.T) {
	if _, err := DecodeEvent([]byte(`"scalar"`)); err == nil {
		t.Fatal("DecodeEvent accepted a non-object")
	}
	if _, err := DecodeEvent([]byte(`{`)); err == nil {
		t.Fatal("DecodeEvent accepted truncated JSON")
	}
}

// EncodeEvent is the exact INVERSE of DecodeEvent: every field survives a
// wire → core → wire → core round trip. This is the pin that lets a front door
// forward an already-decoded event to a core in another process without the
// forwarded bytes drifting from what that core's decoder accepts.
func TestEncodeEvent_RoundTripsThroughDecode(t *testing.T) {
	raw := `{"schemaVersion":"1","id":"evt-1","type":"review-requested","at":"2026-07-16T12:00:00Z","expiresAt":"2026-07-16T12:15:00Z","payload":{"pr":42}}`
	first, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	wire, err := EncodeEvent(first)
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	second, err := DecodeEvent(wire)
	if err != nil {
		t.Fatalf("DecodeEvent(EncodeEvent(...)) = %v; encoded %s", err, wire)
	}
	if second.SchemaVersion != first.SchemaVersion || second.ID != first.ID || second.Type != first.Type {
		t.Fatalf("envelope drifted: %+v -> %+v", first, second)
	}
	if !second.At.Equal(first.At) {
		t.Fatalf("at drifted: %s -> %s", first.At, second.At)
	}
	if !second.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("expiresAt drifted: %s -> %s", first.ExpiresAt, second.ExpiresAt)
	}
	if second.Payload["pr"] != first.Payload["pr"] {
		t.Fatalf("payload drifted: %v -> %v", first.Payload, second.Payload)
	}
}

// A RESOLVED instant comes off a real clock and carries sub-second precision, so
// the round trip MUST NOT truncate it: a forwarded event whose expiry moved by up
// to a second would change the receiving core's INV-EVT-4 verdict.
func TestEncodeEvent_RoundTripPreservesSubSecondPrecision(t *testing.T) {
	at := time.Date(2026, 7, 16, 12, 0, 0, 123456789, time.UTC)
	wire, err := EncodeEvent(Event{ID: "e", Type: "t", At: at, ExpiresAt: at.Add(time.Millisecond)})
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	got, err := DecodeEvent(wire)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if !got.At.Equal(at) || !got.ExpiresAt.Equal(at.Add(time.Millisecond)) {
		t.Fatalf("sub-second precision lost: %s / %s (encoded %s)", got.At, got.ExpiresAt, wire)
	}
}

// The OPTIONAL fields are OMITTED, not emitted empty: event.schema.json closes the
// object and types `at`/`expiresAt` as strings and `payload` as an object, so
// `"expiresAt":""` or `"payload":null` would be a malformed event at the receiving
// core. Omitting an unset instant is also what lets the RECEIVING core apply the
// defaults against its own clock.
func TestEncodeEvent_OmitsAbsentOptionalFields(t *testing.T) {
	wire, err := EncodeEvent(Event{ID: "e", Type: "t"})
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	for _, field := range []string{"at", "expiresAt", "payload", "schemaVersion"} {
		if strings.Contains(string(wire), `"`+field+`"`) {
			t.Fatalf("encoded %s carries an absent optional field %q", wire, field)
		}
	}
	for _, field := range []string{"id", "type"} {
		if !strings.Contains(string(wire), `"`+field+`"`) {
			t.Fatalf("encoded %s is missing the required field %q", wire, field)
		}
	}
}

// The duration-valued field is GONE from the wire (DEC-EVENT-1): nothing this
// encoder emits computes or carries a duration.
func TestEncodeEvent_EmitsNoDurationField(t *testing.T) {
	at := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	wire, err := EncodeEvent(Event{ID: "e", Type: "t", At: at, ExpiresAt: at.Add(15 * time.Minute)})
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	if strings.Contains(string(wire), `"ttl"`) {
		t.Fatalf("encoded %s still carries a duration-valued field", wire)
	}
}

// An Event with no valid wire form is reported at the encoder, not shipped to a
// core to come back as an opaque "malformed" rejection. An UNSET instant is not a
// fault — it is the default — so it is NOT in this list.
func TestEncodeEvent_RejectsInvalidEvent(t *testing.T) {
	cases := map[string]Event{
		"missing id":   {Type: "t"},
		"missing type": {ID: "e"},
	}
	for desc, evt := range cases {
		t.Run(desc, func(t *testing.T) {
			if _, err := EncodeEvent(evt); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("err = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

// An event carrying NEITHER instant has a perfectly valid wire form — it is the
// DEFAULT event, and the receiving core resolves it to born-expired.
func TestEncodeEvent_AcceptsTheDefaultEvent(t *testing.T) {
	if _, err := EncodeEvent(Event{ID: "e", Type: "t"}); err != nil {
		t.Fatalf("the default event has no wire form: %v", err)
	}
}

// A decoded event must be Enqueue-able: the decoder and Validate agree on what a
// usable event is.
func TestDecodeEvent_FeedsEnqueue(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	evt, err := DecodeEvent([]byte(`{"id":"e","type":"t"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	res, err := q.Enqueue(evt)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if res != Enqueued {
		t.Fatalf("result = %v, want Enqueued", res)
	}
}
