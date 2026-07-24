package eventqueue

import (
	"encoding/json"
	"testing"
	"time"
)

// Golden round-trip: an enqueue Record marshals to the expected JSONL shape and
// reconstructs the same Event (turns the WAL line format into a golden example).
func TestRecordGoldenRoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	enq := time.Date(2026, 7, 16, 12, 0, 1, 0, time.UTC)
	e := Event{
		SchemaVersion: "1",
		ID:            "evt-abc123",
		Type:          "review-requested",
		TTL:           15 * time.Minute,
		At:            at,
		Payload:       map[string]any{"note": "hi"},
	}
	rec := recordFromEvent(e, enq)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"op":"enqueue","eventId":"evt-abc123","type":"review-requested","schemaVersion":"1","ttlNanos":900000000000,"at":"2026-07-16T12:00:00Z","enqueuedAt":"2026-07-16T12:00:01Z","payload":{"note":"hi"}}`
	if string(b) != want {
		t.Fatalf("golden mismatch:\n got: %s\nwant: %s", b, want)
	}
	// Round-trip back to a Record and reconstruct the Event.
	var got Record
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	re := got.event()
	if re.ID != e.ID || re.Type != e.Type || re.TTL != e.TTL || re.SchemaVersion != e.SchemaVersion {
		t.Fatalf("event round-trip mismatch: %+v vs %+v", re, e)
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
