package eventqueue

import (
	"errors"
	"testing"
	"time"
)

// DecodeEvent converts the wire shape (ttl duration string, RFC3339 at) into the
// in-core Event.
func TestDecodeEvent_FullEvent(t *testing.T) {
	raw := `{"schemaVersion":"1","id":"evt-1","type":"review-requested","ttl":"15m","at":"2026-07-16T12:00:00Z","payload":{"pr":42}}`
	got, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if got.SchemaVersion != "1" || got.ID != "evt-1" || got.Type != "review-requested" {
		t.Fatalf("envelope fields = %+v", got)
	}
	if got.TTL != 15*time.Minute {
		t.Fatalf("ttl = %s, want 15m", got.TTL)
	}
	want := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if !got.At.Equal(want) {
		t.Fatalf("at = %s, want %s", got.At, want)
	}
	if got.Payload["pr"] != float64(42) {
		t.Fatalf("payload = %v, want the opaque payload carried through", got.Payload)
	}
}

// `at` is OPTIONAL: absent leaves the zero time (the TTL clock origin is ingest
// time, OQ-EVT-TTL-ORIGIN).
func TestDecodeEvent_AtIsOptional(t *testing.T) {
	got, err := DecodeEvent([]byte(`{"id":"e","type":"t","ttl":"5m"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if !got.At.IsZero() {
		t.Fatalf("at = %s, want the zero time when absent", got.At)
	}
}

// Conversions a structural schema cannot express are reported as ErrInvalidEvent,
// so a caller can classify them as a malformed event.
func TestDecodeEvent_Rejections(t *testing.T) {
	cases := map[string]string{
		"missing ttl":     `{"id":"e","type":"t"}`,
		"unparseable ttl": `{"id":"e","type":"t","ttl":"soon"}`,
		"zero ttl":        `{"id":"e","type":"t","ttl":"0s"}`,
		"negative ttl":    `{"id":"e","type":"t","ttl":"-5m"}`,
		"unparseable at":  `{"id":"e","type":"t","ttl":"5m","at":"yesterday"}`,
	}
	for desc, raw := range cases {
		t.Run(desc, func(t *testing.T) {
			if _, err := DecodeEvent([]byte(raw)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("err = %v, want ErrInvalidEvent", err)
			}
		})
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

// A decoded event must be Enqueue-able: the decoder and Validate agree on what a
// usable event is.
func TestDecodeEvent_FeedsEnqueue(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	evt, err := DecodeEvent([]byte(`{"id":"e","type":"t","ttl":"5m"}`))
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
