package eventqueue

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Golden round-trip: an enqueue Record marshals to the expected JSONL shape and
// reconstructs the same Event (turns the WAL line format into a golden example).
// The record holds the RESOLVED instants, so a replay reconstructs the same expiry
// bound instead of re-defaulting it against a clock that has since moved.
func TestRecordGoldenRoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	enq := time.Date(2026, 7, 16, 12, 0, 1, 0, time.UTC)
	e := Event{
		SchemaVersion: "1",
		ID:            "evt-abc123",
		Type:          "review-requested",
		At:            at,
		ExpiresAt:     at.Add(15 * time.Minute),
		Payload:       map[string]any{"note": "hi"},
	}
	rec := recordFromEvent(e, enq)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"op":"enqueue","eventId":"evt-abc123","type":"review-requested","schemaVersion":"1","at":"2026-07-16T12:00:00Z","expiresAt":"2026-07-16T12:15:00Z","enqueuedAt":"2026-07-16T12:00:01Z","payload":{"note":"hi"}}`
	if string(b) != want {
		t.Fatalf("golden mismatch:\n got: %s\nwant: %s", b, want)
	}
	// The duration-valued field is gone from the durable log too (DEC-EVENT-1).
	if strings.Contains(string(b), "ttl") {
		t.Fatalf("WAL line still carries a duration-valued field: %s", b)
	}
	// Round-trip back to a Record and reconstruct the Event.
	var got Record
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	re := got.event()
	if re.ID != e.ID || re.Type != e.Type || re.SchemaVersion != e.SchemaVersion {
		t.Fatalf("event round-trip mismatch: %+v vs %+v", re, e)
	}
	if !re.At.Equal(e.At) || !re.ExpiresAt.Equal(e.ExpiresAt) {
		t.Fatalf("instants did not round-trip: at %s/%s, expiresAt %s/%s", re.At, e.At, re.ExpiresAt, e.ExpiresAt)
	}
}

// An evict Record omits every enqueue/accept field — it is the durable marker that
// an id has LEFT the queue, which replay honours instead of re-deriving retirement
// from the clock (an expired event may still be owed an attempt, INV-EVT-4).
func TestEvictRecordShape(t *testing.T) {
	b, err := json.Marshal(Record{Op: opEvict, EventID: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"op":"evict","eventId":"e1"}`
	if string(b) != want {
		t.Fatalf("evict record = %s, want %s", b, want)
	}
}

// An accept Record omits the enqueue-only fields (compact WAL line).
func TestAcceptRecordShape(t *testing.T) {
	b, err := json.Marshal(Record{Op: opAccept, EventID: "e1", ListenerID: "h"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"op":"accept","eventId":"e1","listenerId":"h"}`
	if string(b) != want {
		t.Fatalf("accept record = %s, want %s", b, want)
	}
}

func TestMemStoreAppendReplay(t *testing.T) {
	m := NewMemStore()
	if recs, err := m.Replay(); err != nil || len(recs) != 0 {
		t.Fatalf("empty replay = %v, %v", recs, err)
	}
	_ = m.Append(Record{Op: opEnqueue, EventID: "a"})
	_ = m.Append(Record{Op: opAccept, EventID: "a", ListenerID: "h"})
	recs, err := m.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].EventID != "a" || recs[1].Op != opAccept {
		t.Fatalf("replay = %+v", recs)
	}
	// Replay returns a copy: mutating it must not corrupt the store.
	recs[0].EventID = "mutated"
	again, _ := m.Replay()
	if again[0].EventID != "a" {
		t.Fatalf("Replay did not return a defensive copy")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
