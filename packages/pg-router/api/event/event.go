// Package event is the published contract of the router's event inputs: the
// per-record item a command source writes to stdout (the source's reply to a
// query), the optional acknowledgement handle that record may carry, and the
// event wire message (the event schema) that flows from a source through the
// core's durable queue to a handler.
//
// It is a contract package: it holds the message types, the constants and the
// codec and nothing else. It imports the standard library only; the closure
// test fails when that stops being true. The JSON Schema
// schemas/event.schema.json stays hand-written; the both-ways test in this
// package keeps the type and the schema in step. The engine's own queue event
// (internal/event) stays inside the router and is built from these types.
package event

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Ack is the acknowledgement handle of an item: the lane (one independent
// ordered feed of a query) and the token (the item's position in it), both
// non-empty strings the core never interprets and hands back to the source's
// settle subcommand. A handle with an empty lane or token is not a handle.
type Ack struct {
	Lane  string `json:"lane"`
	Token string `json:"token"`
}

// Valid reports whether the handle carries both a lane and a token.
func (a *Ack) Valid() bool { return a != nil && a.Lane != "" && a.Token != "" }

// UnmarshalJSON decodes an ack leniently: a value that is not an object with
// string lane and token leaves the handle empty and is never an error, so a
// malformed handle only means the item carries none and never fails the parse
// of the record around it.
func (a *Ack) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		*a = Ack{}
		return nil
	}
	lane, _ := m["lane"].(string)
	token, _ := m["token"].(string)
	*a = Ack{Lane: lane, Token: token}
	return nil
}

// Item is the per-record shape a command source's stdout decodes into. Type is
// the item classification, carried through to the engine's item. At and
// ExpiresAt are OPTIONAL RFC 3339 timestamp strings, camelCase to match the
// event wire's own at and expiresAt. Emit is an OPTIONAL event-type selector:
// when present it MUST be one of the query's declared emits; absent, the record
// falls back to the query's first declared emit.
type Item struct {
	ID        string         `json:"id"`
	Emit      string         `json:"emit,omitempty"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Metadata  map[string]any `json:"metadata"`
	At        string         `json:"at,omitempty"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
	// Ack is the optional acknowledgement handle, set only by a source running
	// with deferred acknowledgement.
	Ack *Ack `json:"ack,omitempty"`
}

// Handle returns the item's acknowledgement handle, nil unless both its lane and
// token are non-empty.
func (it Item) Handle() *Ack {
	if !it.Ack.Valid() {
		return nil
	}
	return &Ack{Lane: it.Ack.Lane, Token: it.Ack.Token}
}

// Event is the event wire message (schemas/event.schema.json): a typed fact that
// flows from a source through the durable queue to a handler. The instants are
// RFC 3339 strings; the semantic parse is the queue's job.
type Event struct {
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	At            string         `json:"at,omitempty"`
	ExpiresAt     string         `json:"expiresAt,omitempty"`
	NotBefore     string         `json:"notBefore,omitempty"`
	Generation    int            `json:"generation,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// EncodeItems encodes items as one JSON array, the whole output of a source
// subcommand. A nil slice encodes as [], never null.
func EncodeItems(items []Item) ([]byte, error) {
	if items == nil {
		items = []Item{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(items); err != nil {
		return nil, fmt.Errorf("encode items: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeItem decodes one record of a source's output.
func DecodeItem(data []byte) (Item, error) {
	var it Item
	if err := json.Unmarshal(data, &it); err != nil {
		return Item{}, fmt.Errorf("decode item: %w", err)
	}
	return it, nil
}

// Encode encodes an event wire message.
func Encode(e Event) ([]byte, error) { return json.Marshal(e) }

// Decode strictly decodes an event wire message: an unknown property is an
// error.
func Decode(data []byte) (Event, error) {
	var e Event
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	return e, nil
}
