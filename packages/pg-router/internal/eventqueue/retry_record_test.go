package eventqueue

import (
	"path/filepath"
	"testing"
	"time"
)

func openRetryQueue(t *testing.T, path string) (*Queue, *FileStore) {
	t.Helper()
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(fs)
	if err != nil {
		_ = fs.Close()
		t.Fatal(err)
	}
	return q, fs
}

func countOps(t *testing.T, fs *FileStore, op opKind) int {
	t.Helper()
	recs, err := fs.Replay()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Op == op {
			n++
		}
	}
	return n
}

// The retry count written when a re-run is scheduled survives a restart of a real
// file-backed queue, and a second restart after more retries sees the new count.
func TestRetryCountSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q1, fs1 := openRetryQueue(t, path)
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(time.Hour)))
	if err := q1.RecordRetry("e1", "h", 1); err != nil {
		t.Fatal(err)
	}
	if got := q1.RetryCount("e1", "h"); got != 1 {
		t.Fatalf("RetryCount = %d, want 1", got)
	}
	_ = fs1.Close()

	q2, fs2 := openRetryQueue(t, path)
	if got := q2.RetryCount("e1", "h"); got != 1 {
		t.Fatalf("after restart RetryCount = %d, want 1", got)
	}
	if got := q2.RetryCount("e1", "other"); got != 0 {
		t.Fatalf("other listener RetryCount = %d, want 0", got)
	}
	if err := q2.RecordRetry("e1", "h", 2); err != nil {
		t.Fatal(err)
	}
	_ = fs2.Close()

	q3, fs3 := openRetryQueue(t, path)
	defer func() { _ = fs3.Close() }()
	if got := q3.RetryCount("e1", "h"); got != 2 {
		t.Fatalf("after second restart RetryCount = %d, want 2", got)
	}
}

// Compaction keeps the count of a live event and drops the records of an event that
// is gone, and a compacted log replays to the same count.
func TestRetryRecordsCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q, fs := openRetryQueue(t, path)
	mustEnqueue(t, q, evtUntil("live", "T", time.Now().Add(time.Hour)))
	mustEnqueue(t, q, evtUntil("gone", "T", time.Now().Add(time.Hour)))
	for _, id := range []string{"live", "gone"} {
		for n := 1; n <= 2; n++ {
			if err := q.RecordRetry(id, "h", n); err != nil {
				t.Fatal(err)
			}
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
	if got := countOps(t, fs, opRetry); got != 4 {
		t.Fatalf("retry records before compaction = %d, want 4", got)
	}
	if _, err := fs.Compact(); err != nil {
		t.Fatal(err)
	}
	recs, err := fs.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var retries []Record
	for _, r := range recs {
		if r.Op == opRetry {
			retries = append(retries, r)
		}
	}
	if len(retries) != 1 || retries[0].EventID != "live" || retries[0].RetryCount != 2 {
		t.Fatalf("retry records after compaction = %+v, want one for live with count 2", retries)
	}
	_ = fs.Close()
	q2, fs2 := openRetryQueue(t, path)
	defer func() { _ = fs2.Close() }()
	if got := q2.RetryCount("live", "h"); got != 2 {
		t.Fatalf("replayed compacted count = %d, want 2", got)
	}
	if got := q2.RetryCount("gone", "h"); got != 0 {
		t.Fatalf("gone event count = %d, want 0", got)
	}
}

// A decline and a first attempt write no retry record; an accept settles the
// listener, so its retry record is dropped on replay and by compaction.
func TestRetryRecordNotWrittenForDeclineAndDroppedOnAccept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q, fs := openRetryQueue(t, path)
	mustEnqueue(t, q, evtUntil("e1", "T", time.Now().Add(time.Hour)))
	l := newListener("h", "T")
	l.busyRemaining["e1"] = 1
	q.Register(l)
	q.Dispatch() // a first attempt that is declined busy
	if len(l.offered) != 1 {
		t.Fatalf("offered = %v, want one first attempt", l.offered)
	}
	if got := countOps(t, fs, opRetry); got != 0 {
		t.Fatalf("retry records after a decline = %d, want 0", got)
	}
	if err := q.RecordRetry("e1", "h", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Append(Record{Op: opAccept, EventID: "e1", ListenerID: "h", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
	q2, fs2 := openRetryQueue(t, path)
	defer func() { _ = fs2.Close() }()
	if got := q2.RetryCount("e1", "h"); got != 0 {
		t.Fatalf("count after accept = %d, want 0", got)
	}
	if _, err := fs2.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := countOps(t, fs2, opRetry); got != 0 {
		t.Fatalf("retry records after accept and compaction = %d, want 0", got)
	}
}
