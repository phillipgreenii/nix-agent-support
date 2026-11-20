package status_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/pg-router/api/status"
	"github.com/phillipgreenii/pg-router/conformance/golden"
	"github.com/phillipgreenii/pg-router/schemas"
)

const messageType = "cli.status-reply"

// The both-ways test, type to schema: every golden fixture decodes into Reply
// with unknown properties rejected, and what Encode writes back validates
// against the hand-written schema and decodes to the same value.
func TestGoldenDecodesStrictlyAndRoundTripsThroughTheSchema(t *testing.T) {
	m, err := golden.Golden(messageType)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := status.Decode(raw)
	if err != nil {
		t.Fatalf("golden fixture does not decode strictly: %v", err)
	}
	encoded, err := status.Encode(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas.ValidateBytes(messageType, encoded); err != nil {
		t.Fatalf("encoded reply fails its schema: %v\n%s", err, encoded)
	}
	again, err := status.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	againEncoded, err := status.Encode(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, againEncoded) {
		t.Fatalf("round trip changed the reply:\n got %s\nwant %s", againEncoded, encoded)
	}
}

// A reply with nothing filled in still encodes to a schema-valid body: the
// always-present arrays and objects are empty, never null.
func TestZeroReplyEncodesToAValidBody(t *testing.T) {
	encoded, err := status.Encode(status.Reply{QueueLog: status.QueueLog{State: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas.ValidateBytes(messageType, encoded); err != nil {
		t.Fatalf("zero reply fails its schema: %v\n%s", err, encoded)
	}
}

// Schema to type: an unknown property is rejected by the strict decoder.
func TestDecodeRejectsUnknownProperty(t *testing.T) {
	if _, err := status.Decode([]byte(`{"schemaVersion":"1","surprise":true}`)); err == nil {
		t.Fatal("Decode accepted an unknown property")
	}
}

// The version exists once: the schemas package re-exports this constant.
func TestSchemaVersionIsSharedWithSchemas(t *testing.T) {
	if status.SchemaVersion != schemas.SchemaVersion {
		t.Fatalf("status %q != schemas %q", status.SchemaVersion, schemas.SchemaVersion)
	}
	if status.SchemaVersion != "1" {
		t.Fatalf("schema version = %q, want the unchanged value 1", status.SchemaVersion)
	}
}
