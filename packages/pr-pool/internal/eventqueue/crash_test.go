package eventqueue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dropAcceptStore wraps a Store but silently DROPS accept records — modeling a
// crash in the window between the in-memory accept and the durable accept write
// (ADR 0031 req 4). Enqueue records still persist, so no accepted event is lost.
type dropAcceptStore struct {
	inner Store
}

func (d *dropAcceptStore) Append(r Record) error {
	if r.Op == opAccept {
		return nil // lost to the crash window
	}
	return d.inner.Append(r)
}
func (d *dropAcceptStore) Replay() ([]Record, error) { return d.inner.Replay() }
func (d *dropAcceptStore) Close() error              { return d.inner.Close() }

// Kill-mid-write: an event accepted just before a crash (accept record lost) is
// NOT lost — it is redelivered exactly once on restart, and an idempotent
// handler absorbs the duplicate (INV-EVT-1 / INV-EVT-2, ADR 0031 req 4).
func TestCrashWindowRedeliversAcceptedEventExactlyOnce(t *testing.T) {
	mem := NewMemStore()
	store := &dropAcceptStore{inner: mem}

	// Process 1: enqueue + accept, but the accept record is lost to the crash.
	q1, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	l1 := newListener("h", "T")
	q1.Register(l1)
	mustEnqueue(t, q1, evt("e1", "T", time.Hour))
	q1.Dispatch()
	if !equal(l1.accepted, []string{"e1"}) {
		t.Fatalf("pre-crash accept failed: %v", l1.accepted)
	}

	// Simulate restart: a fresh queue replays the SAME durable log (which holds
	// the enqueue record but not the lost accept record). The event survives and
	// is re-offered exactly once.
	q2, err := New(mem) // replay from the real underlying store (enqueue persisted)
	if err != nil {
		t.Fatal(err)
	}
	if q2.DepthByType()["T"] != 1 {
		t.Fatalf("accepted event was LOST across restart: %v", q2.DepthByType())
	}
	l2 := newListener("h", "T")
	q2.Register(l2)
	q2.Dispatch() // redelivery
	q2.Dispatch() // must NOT deliver a second time (at-most-one redelivery)
	if !equal(l2.offered, []string{"e1"}) {
		t.Fatalf("redelivery count = %v, want exactly one re-offer of e1", l2.offered)
	}
}

// A normal restart with a fully-persisted log redelivers nothing: an accepted
// event whose accept record IS persisted stays accepted across the restart.
func TestRestartWithPersistedAcceptDoesNotRedeliver(t *testing.T) {
	mem := NewMemStore()
	q1, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	l1 := newListener("h", "T")
	q1.Register(l1)
	mustEnqueue(t, q1, evt("e1", "T", time.Hour))
	q1.Dispatch()

	q2, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	l2 := newListener("h", "T")
	q2.Register(l2)
	q2.Dispatch()
	if len(l2.offered) != 0 {
		t.Fatalf("persisted-accept event was redelivered: %v", l2.offered)
	}
}

// FileStore survives a real process restart: a fresh queue over the same WAL
// file recovers the enqueued (unaccepted) event and delivers it.
func TestFileStoreRestartRecoversEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.wal")

	s1, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	q1, err := New(s1)
	if err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q1, evt("e1", "T", time.Hour))
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: new FileStore over the same path, replay.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	q2, err := New(s2)
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q2.Register(l)
	q2.Dispatch()
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("event not recovered from WAL across restart: %v", l.accepted)
	}
}

// A torn trailing record (crash mid-write) is tolerated: everything before it
// replays intact, the partial tail is discarded.
func TestFileStoreTornTailTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.wal")

	s1, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	q1, err := New(s1)
	if err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q1, evt("e1", "T", time.Hour))
	_ = s1.Close()

	// Append a half-written (torn) JSON line, as a crash mid-write would leave.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"enqueue","eventId":"e2","typ`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	q2, err := New(s2)
	if err != nil {
		t.Fatalf("replay did not tolerate torn tail: %v", err)
	}
	if q2.DepthByType()["T"] != 1 {
		t.Fatalf("intact record before torn tail was lost: %v", q2.DepthByType())
	}
}
