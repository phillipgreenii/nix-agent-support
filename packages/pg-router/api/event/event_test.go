package event_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/api/event"
	"github.com/phillipgreenii/pg-router/conformance/golden"
	"github.com/phillipgreenii/pg-router/schemas"
)

// The both-ways test, type to schema: the golden event decodes strictly into
// Event, and what Encode writes back validates against the hand-written schema
// and round-trips to the same bytes.
func TestGoldenEventRoundTripsThroughTheSchema(t *testing.T) {
	m, err := golden.Golden("event")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	e, err := event.Decode(raw)
	if err != nil {
		t.Fatalf("golden fixture does not decode strictly: %v", err)
	}
	encoded, err := event.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas.ValidateBytes("event", encoded); err != nil {
		t.Fatalf("encoded event fails its schema: %v\n%s", err, encoded)
	}
	again, err := event.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	againEncoded, _ := event.Encode(again)
	if !bytes.Equal(encoded, againEncoded) {
		t.Fatalf("round trip changed the event:\n got %s\nwant %s", againEncoded, encoded)
	}
}

// Schema to type: an unknown property is rejected by the strict decoder, as the
// schema's additionalProperties:false rejects it.
func TestDecodeRejectsUnknownProperty(t *testing.T) {
	body := []byte(`{"id":"e","type":"t","surprise":1}`)
	if _, err := event.Decode(body); err == nil {
		t.Fatal("Decode accepted an unknown property")
	}
	if err := schemas.ValidateBytes("event", body); err == nil {
		t.Fatal("the schema accepted an unknown property")
	}
}

func TestItemRoundTripWithAck(t *testing.T) {
	in := event.Item{
		ID: "a", Emit: "e", Type: "t", Title: "T", Metadata: map[string]any{"k": "v"},
		At: "2026-01-02T03:04:05Z", ExpiresAt: "2026-01-02T03:14:05Z",
		Ack: &event.Ack{Lane: "l", Token: "7"},
	}
	b, err := event.EncodeItems([]event.Item{in})
	if err != nil {
		t.Fatal(err)
	}
	var got []event.Item
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Handle() == nil || *got[0].Handle() != *in.Ack || got[0].ID != "a" || got[0].Emit != "e" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestEncodeItemsNilIsEmptyArray(t *testing.T) {
	b, err := event.EncodeItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("got %q, want []", b)
	}
}

func TestOmittedOptionalFieldsAreAbsent(t *testing.T) {
	b, _ := event.EncodeItems([]event.Item{{ID: "a", Type: "t", Title: "T", Metadata: map[string]any{}}})
	for _, key := range []string{"emit", "at", "expiresAt", "ack"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Fatalf("%s present in %s", key, b)
		}
	}
}

// A malformed ack never fails the record: the item simply carries no handle.
func TestMalformedAckYieldsNoHandle(t *testing.T) {
	for _, body := range []string{
		`{"id":"a","ack":"nope"}`,
		`{"id":"a","ack":{"lane":"l"}}`,
		`{"id":"a","ack":{"lane":1,"token":"t"}}`,
		`{"id":"a","ack":[1]}`,
		`{"id":"a","ack":null}`,
	} {
		it, err := event.DecodeItem([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if it.Handle() != nil {
			t.Fatalf("%s: handle = %+v, want none", body, it.Handle())
		}
	}
	it, _ := event.DecodeItem([]byte(`{"id":"a","ack":{"lane":"l","token":"t"}}`))
	if h := it.Handle(); h == nil || h.Lane != "l" || h.Token != "t" {
		t.Fatalf("valid ack handle = %+v", h)
	}
}
