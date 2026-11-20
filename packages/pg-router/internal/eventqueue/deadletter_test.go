package eventqueue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var deadAt = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func dl(id, role string) DeadLetter {
	return DeadLetter{EventID: id, Role: role, Class: ClassDeterministic, Attempts: 1, Reason: DeadReasonDeterministic, At: deadAt}
}

// One terminal failure appends exactly one failed record, which survives a restart.
func TestDeadLetterRecordedAndReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q1, fs1 := openRetryQueue(t, path)
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(time.Hour)))
	if err := q1.RecordFailed(dl("e1", "h")); err != nil {
		t.Fatal(err)
	}
	if got := countOps(t, fs1, opFailed); got != 1 {
		t.Fatalf("failed records = %d, want 1", got)
	}
	_ = fs1.Close()

	q2, fs2 := openRetryQueue(t, path)
	defer func() { _ = fs2.Close() }()
	got := q2.DeadLetters()
	if len(got) != 1 || got[0] != dl("e1", "h") {
		t.Fatalf("DeadLetters after restart = %+v, want [%+v]", got, dl("e1", "h"))
	}
}

// A failed record for an event the queue does not hold is not written.
func TestDeadLetterUnheldEventIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q, fs := openRetryQueue(t, path)
	defer func() { _ = fs.Close() }()
	if err := q.RecordFailed(dl("nope", "h")); err != nil {
		t.Fatal(err)
	}
	if got := countOps(t, fs, opFailed); got != 0 {
		t.Fatalf("failed records = %d, want 0", got)
	}
}

// Eviction drops the record with the event, in replay and in compaction; a live
// event's record is kept through compaction.
func TestDeadLetterDroppedWithEventOnEvictionAndCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q, fs := openRetryQueue(t, path)
	mustEnqueue(t, q, evtUntil("live", "T", time.Now().Add(time.Hour)))
	mustEnqueue(t, q, evtUntil("gone", "T", time.Now().Add(time.Hour)))
	for _, id := range []string{"live", "gone"} {
		if err := q.RecordFailed(dl(id, "h")); err != nil {
			t.Fatal(err)
		}
	}
	q.mu.Lock()
	rec := q.recordEvictLocked("gone", EvictReasonRetired)
	delete(q.entries, "gone")
	q.dropFromOrder("gone")
	err := q.appendLocked(rec)
	q.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := q.DeadLetters(); len(got) != 1 || got[0].EventID != "live" {
		t.Fatalf("DeadLetters after eviction = %+v, want only live", got)
	}
	// The verb's reader (a fold of the file) agrees before compaction.
	if got, err := ReadDeadLettersFile(path); err != nil || len(got) != 1 || got[0].EventID != "live" {
		t.Fatalf("ReadDeadLettersFile before compaction = %+v, %v", got, err)
	}
	if got := countOps(t, fs, opFailed); got != 2 {
		t.Fatalf("failed records before compaction = %d, want 2", got)
	}
	if _, err := fs.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := countOps(t, fs, opFailed); got != 1 {
		t.Fatalf("failed records after compaction = %d, want 1", got)
	}
	_ = fs.Close()
	q2, fs2 := openRetryQueue(t, path)
	defer func() { _ = fs2.Close() }()
	if got := q2.DeadLetters(); len(got) != 1 || got[0] != dl("live", "h") {
		t.Fatalf("DeadLetters after compacted restart = %+v", got)
	}
}

// ReadDeadLettersFile lists exactly the held records, tolerates a torn last line,
// and reads a missing log as empty.
func TestReadDeadLettersFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	if got, err := ReadDeadLettersFile(path); err != nil || len(got) != 0 {
		t.Fatalf("missing log = %+v, %v; want empty, nil", got, err)
	}
	q, fs := openRetryQueue(t, path)
	mustEnqueue(t, q, evtUntil("e1", "T", time.Now().Add(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", time.Now().Add(time.Hour)))
	for _, d := range []DeadLetter{dl("e2", "b"), dl("e1", "a")} {
		if err := q.RecordFailed(d); err != nil {
			t.Fatal(err)
		}
	}
	// A reader works while the writer still holds the log open (no lock taken).
	got, err := ReadDeadLettersFile(path)
	if err != nil || len(got) != 2 || got[0] != dl("e1", "a") || got[1] != dl("e2", "b") {
		t.Fatalf("ReadDeadLettersFile = %+v, %v", got, err)
	}
	_ = fs.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"failed","eventId":"e1","listenerId":"torn`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got, err = ReadDeadLettersFile(path)
	if err != nil || len(got) != 2 {
		t.Fatalf("with a torn last line = %+v, %v; want the two intact records", got, err)
	}
	if _, err := ReadDeadLettersFile(t.TempDir()); err == nil {
		t.Fatal("reading a directory as the log must fail")
	}
}

// A one-shot dispatch's records make the dead letter visible, and the writer
// refuses while another holder owns the log.
func TestRecordSettledFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	evt := Event{ID: "e1", Type: "T", Payload: map[string]any{}}
	if err := RecordSettledFailure(path, evt, dl("e1", "h")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDeadLettersFile(path)
	if err != nil || len(got) != 1 || got[0] != dl("e1", "h") {
		t.Fatalf("ReadDeadLettersFile = %+v, %v", got, err)
	}
	_, fs := openRetryQueue(t, path)
	defer func() { _ = fs.Close() }()
	if err := RecordSettledFailure(path, evt, dl("e1", "h")); err == nil {
		t.Fatal("RecordSettledFailure must fail while the log is locked")
	}
}
