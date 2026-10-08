package store_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// The tests of this package that need the fault FS are external, which is how
// the import cycle between store and storefault is avoided. They build their
// logs with event.Encode, so a fixture cannot drift from the codec.

var epoch = time.Date(2026, 10, 7, 13, 30, 4, 120_000_000, time.UTC)

// failureAt is the instant the stores of these tests report for "now".
var failureAt = time.Date(2026, 10, 8, 12, 30, 45, 0, time.UTC)

const testTask = event.TaskID("day:2026-10-07:post-plan")

func newID(kind byte, n int) event.ID {
	var entropy [10]byte
	entropy[0] = kind
	binary.BigEndian.PutUint32(entropy[6:], uint32(n))
	return event.NewID(epoch, bytes.NewReader(entropy[:]))
}

func batchID(n int) event.ID { return newID('b', n) }

func envelope(n int) event.Envelope {
	return event.Envelope{ID: newID('e', n), At: event.At(epoch), EffectiveAt: event.At(epoch)}
}

// plainEvent is an event that is never part of a batch.
func plainEvent(n int) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.TaskCompleted{TaskID: testTask}}
}

func memberEvent(n int, b event.ID) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.TaskMissed{TaskID: testTask, Batch: b}}
}

func commitEvent(n int, b event.ID) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.BatchCommitted{Batch: b}}
}

// batchOf is a batch of members member events numbered from first, and its
// batch.committed.
func batchOf(first, members int, b event.ID) []event.Event {
	var out []event.Event
	for i := 0; i < members; i++ {
		out = append(out, memberEvent(first+i, b))
	}
	return append(out, commitEvent(first+members, b))
}

// line is the newline-terminated encoding of e.
func line(t testing.TB, e event.Event) []byte {
	t.Helper()
	b, err := event.Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return append(b, '\n')
}

func lines(t testing.TB, evs ...event.Event) []byte {
	t.Helper()
	var out []byte
	for _, e := range evs {
		out = append(out, line(t, e)...)
	}
	return out
}

func ids(evs []event.Event) []event.ID {
	out := make([]event.ID, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

// seedLog writes data as the log of a new data directory and returns the
// directory.
func seedLog(t testing.TB, data []byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func logPath(dir string) string { return filepath.Join(dir, "events.jsonl") }

func readLog(t testing.TB, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(logPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixedNow() time.Time { return failureAt }

// openStore opens dir through fs (nil means the OS) with the fixed clock and
// closes the store when the test ends.
func openStore(t testing.TB, dir string, fs store.FS) (*store.Store, []event.Event, store.Recovery) {
	t.Helper()
	s, evs, rec, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, evs, rec
}

// mutating returns the Write, Sync and Truncate calls of fs, which are the
// calls whose order matters, as "Op name" strings with directories dropped.
func mutating(fs *storefault.FS) []string {
	var out []string
	for _, c := range fs.Calls() {
		switch c.Op {
		case storefault.OpWrite, storefault.OpSync, storefault.OpTruncate:
			out = append(out, string(c.Op)+" "+filepath.Base(c.Name))
		}
	}
	return out
}

// sidecars lists the recovery sidecars of dir, by name.
func sidecars(t testing.TB, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "events.jsonl.recovered-*"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range matches {
		matches[i] = filepath.Base(matches[i])
	}
	return matches
}
