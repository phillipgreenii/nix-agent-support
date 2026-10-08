package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// The scan tests build their lines with event.Encode, so a fixture cannot
// drift from the codec. Only a malformed line is a hand-written file under
// testdata/logs/store.

var epoch = time.Date(2026, 10, 7, 13, 30, 4, 120_000_000, time.UTC)

const testTask = event.TaskID("day:2026-10-07:post-plan")

// fataler is what the helpers need of a *testing.T and of a *rapid.T.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// newID returns a valid ULID that is a function of kind and n only.
func newID(kind byte, n int) event.ID {
	var entropy [10]byte
	entropy[0] = kind
	binary.BigEndian.PutUint32(entropy[6:], uint32(n))
	return event.NewID(epoch, bytes.NewReader(entropy[:]))
}

func eventID(n int) event.ID { return newID('e', n) }
func batchID(n int) event.ID { return newID('b', n) }

func envelope(n int) event.Envelope {
	return event.Envelope{ID: eventID(n), At: event.At(epoch), EffectiveAt: event.At(epoch)}
}

// plainEvent is an event that is never part of a batch.
func plainEvent(n int) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.TaskCompleted{TaskID: testTask}}
}

// memberEvent is an event of batch b.
func memberEvent(n int, b event.ID) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.TaskMissed{TaskID: testTask, Batch: b}}
}

func commitEvent(n int, b event.ID) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.BatchCommitted{Batch: b}}
}

// retractBatchEvent names a batch as target_batch, which is not membership.
func retractBatchEvent(n int, b event.ID) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.EventRetracted{TargetBatch: b, Reason: "wrong day"}}
}

func annotatedEvent(n int, note string) event.Event {
	return event.Event{Envelope: envelope(n), Payload: event.CycleAnnotated{CycleID: event.CycleID(newID('c', 1)), Note: note}}
}

// enc is the newline-terminated line of e.
func enc(t fataler, e event.Event) []byte {
	t.Helper()
	line, err := event.Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return append(line, '\n')
}

// batchLines is a committed batch of members member events and its commit,
// numbered from first.
func batchLines(t fataler, first, members int, b event.ID) [][]byte {
	t.Helper()
	var out [][]byte
	for i := 0; i < members; i++ {
		out = append(out, enc(t, memberEvent(first+i, b)))
	}
	return append(out, enc(t, commitEvent(first+members, b)))
}

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func joinAll(groups ...[][]byte) []byte {
	var all [][]byte
	for _, g := range groups {
		all = append(all, g...)
	}
	return join(all...)
}

func dropNewline(line []byte) []byte { return bytes.TrimSuffix(line, []byte("\n")) }

// fixture reads a hand-written malformed line, newline included.
func fixture(t fataler, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", "store", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) || bytes.Count(data, []byte("\n")) != 1 {
		t.Fatalf("fixture %s must be exactly one newline-terminated line", name)
	}
	return data
}

func mustScan(t fataler, data []byte) ([]event.Event, int64, scanReport) {
	t.Helper()
	events, end, rep, err := scan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return events, end, rep
}

func wantCorrupt(t *testing.T, err error, line int) *CorruptError {
	t.Helper()
	var ce *CorruptError
	if !errors.As(err, &ce) {
		t.Fatalf("scan error = %v (%T), want a *CorruptError", err, err)
	}
	if ce.Line != line {
		t.Fatalf("CorruptError.Line = %d, want %d (%v)", ce.Line, line, err)
	}
	if ce.Cause == nil {
		t.Fatalf("CorruptError at line %d has no cause", ce.Line)
	}
	if !errors.Is(err, ce.Cause) {
		t.Fatalf("errors.Is(err, Cause) is false: Unwrap must return the cause")
	}
	if !strings.Contains(err.Error(), "line "+strconv.Itoa(line)) {
		t.Errorf("message %q does not name line %d", err.Error(), line)
	}
	return ce
}

func wantLines(t *testing.T, events []event.Event, ids ...event.ID) {
	t.Helper()
	if len(events) != len(ids) {
		t.Fatalf("scan returned %d events, want %d", len(events), len(ids))
	}
	for i, e := range events {
		if e.ID != ids[i] {
			t.Errorf("event %d id = %s, want %s", i, e.ID, ids[i])
		}
		if e.Line != i+1 {
			t.Errorf("event %d Line = %d, want %d", i, e.Line, i+1)
		}
	}
}

// INV-LOG-9: a log whose every line is acknowledged scans completely.
func TestScanCleanLog(t *testing.T) {
	b1 := batchID(1)
	data := joinAll(
		[][]byte{enc(t, plainEvent(1))},
		batchLines(t, 2, 2, b1),
		[][]byte{enc(t, plainEvent(5)), enc(t, retractBatchEvent(6, b1))},
	)
	events, end, rep := mustScan(t, data)
	wantLines(t, events, eventID(1), eventID(2), eventID(3), eventID(4), eventID(5), eventID(6))
	if events[3].Type != event.TypeBatchCommitted {
		t.Errorf("event 4 is %s: batch.committed events are part of the result", events[3].Type)
	}
	if end != int64(len(data)) {
		t.Errorf("endOfLastCommitted = %d, want the file size %d", end, len(data))
	}
	want := scanReport{Lines: 6, Size: int64(len(data)), Batches: 1}
	if !reflect.DeepEqual(rep, want) {
		t.Errorf("report = %+v, want %+v", rep, want)
	}

	t.Run("an empty log is clean", func(t *testing.T) {
		events, end, rep := mustScan(t, nil)
		if len(events) != 0 || end != 0 || !reflect.DeepEqual(rep, scanReport{}) {
			t.Errorf("empty log: %d events, end %d, report %+v", len(events), end, rep)
		}
	})
}

// INV-LOG-9: a final line with no terminating newline was never acknowledged.
func TestScanTornFinalLine(t *testing.T) {
	committed := join(enc(t, plainEvent(1)), joinAll(batchLines(t, 2, 1, batchID(1))))
	next := enc(t, plainEvent(10))
	cases := []struct {
		name string
		tail []byte
	}{
		{"half of a line", next[:len(next)/2]},
		{"one byte", next[:1]},
		{"a complete line missing only its newline", dropNewline(next)},
		{"half of a batch member", enc(t, memberEvent(11, batchID(2)))[:20]},
		{"half of a batch.committed", enc(t, commitEvent(12, batchID(1)))[:40]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := join(committed, c.tail)
			events, end, rep := mustScan(t, data)
			wantLines(t, events, eventID(1), eventID(2), eventID(3))
			if end != int64(len(committed)) {
				t.Errorf("endOfLastCommitted = %d, want the offset before the torn line, %d", end, len(committed))
			}
			if !rep.TornTail || rep.TornLine != 4 || rep.TornStart != int64(len(committed)) || rep.TornBytes != int64(len(c.tail)) {
				t.Errorf("torn report = {%v line %d start %d bytes %d}, want {true line 4 start %d bytes %d}",
					rep.TornTail, rep.TornLine, rep.TornStart, rep.TornBytes, len(committed), len(c.tail))
			}
			if rep.TornCause == nil {
				t.Errorf("a torn tail must say why")
			}
			if rep.Lines != 4 || rep.Size != int64(len(data)) || rep.UncommittedEvents != 0 || rep.Batches != 1 {
				t.Errorf("report = %+v", rep)
			}
		})
	}
}

// INV-LOG-9 (operator ruling 2026-10-08): a final line that ends in a newline
// but does not decode is a torn tail; INV-LOG-11: followed by another line it
// is corruption; INV-LOG-2: an unknown version is never torn.
func TestScanCompleteButUnparsableFinalLineIsTornTail(t *testing.T) {
	committed := join(enc(t, plainEvent(1)), enc(t, plainEvent(2)))
	after := enc(t, plainEvent(3))
	bad := map[string][]byte{
		"garbage":                             fixture(t, "garbage-line.jsonl"),
		"JSON that fails its schema":          fixture(t, "schema-fail-line.jsonl"),
		"a truncated line that has a newline": fixture(t, "truncated-line.jsonl"),
		"an event.retracted carrying a batch": fixture(t, "retracted-with-batch-line.jsonl"),
	}
	for name, line := range bad {
		t.Run(name+" as the final line is torn", func(t *testing.T) {
			data := join(committed, line)
			events, end, rep := mustScan(t, data)
			wantLines(t, events, eventID(1), eventID(2))
			if end != int64(len(committed)) {
				t.Errorf("endOfLastCommitted = %d, want %d", end, len(committed))
			}
			if !rep.TornTail || rep.TornLine != 3 || rep.TornStart != int64(len(committed)) || rep.TornBytes != int64(len(line)) {
				t.Errorf("report = %+v, want a torn tail of %d bytes at line 3, offset %d", rep, len(line), len(committed))
			}
			if rep.TornCause == nil || strings.Contains(rep.TornCause.Error(), "no terminating newline") {
				t.Errorf("TornCause = %v, want the decode failure", rep.TornCause)
			}
		})
		t.Run(name+" followed by another line is corrupt", func(t *testing.T) {
			_, _, _, err := scan(bytes.NewReader(join(committed, line, after)))
			_ = wantCorrupt(t, err, 3)
		})
	}

	t.Run("an unknown version as the final line is never torn", func(t *testing.T) {
		_, _, _, err := scan(bytes.NewReader(join(committed, fixture(t, "unknown-version-line.jsonl"))))
		var uv *UnknownVersionError
		if !errors.As(err, &uv) || uv.Line != 3 || uv.V != 2 {
			t.Fatalf("scan error = %v, want *UnknownVersionError at line 3 with v 2", err)
		}
	})
	t.Run("an unknown version with no newline is never torn", func(t *testing.T) {
		line := dropNewline(fixture(t, "unknown-version-line.jsonl"))
		_, _, _, err := scan(bytes.NewReader(join(committed, line)))
		var uv *UnknownVersionError
		if !errors.As(err, &uv) || uv.Line != 3 {
			t.Fatalf("scan error = %v, want *UnknownVersionError at line 3", err)
		}
	})
}

// INV-LOG-9: the trailing events of a batch with no batch.committed were never
// acknowledged.
func TestScanUncommittedTrailingBatch(t *testing.T) {
	committed := join(enc(t, plainEvent(1)), joinAll(batchLines(t, 2, 1, batchID(1))))
	b2 := batchID(2)
	members := join(enc(t, memberEvent(10, b2)), enc(t, memberEvent(11, b2)), enc(t, memberEvent(12, b2)))

	check := func(t *testing.T, rep scanReport, end int64, wantEnd int64, wantMembers int, firstLine int) {
		t.Helper()
		if end != wantEnd {
			t.Errorf("endOfLastCommitted = %d, want the offset before the batch's first event, %d", end, wantEnd)
		}
		if rep.UncommittedEvents != wantMembers || rep.UncommittedLine != firstLine || rep.UncommittedStart != wantEnd || rep.UncommittedBatch != b2 {
			t.Errorf("uncommitted = {%d events, line %d, start %d, batch %s}, want {%d, %d, %d, %s}",
				rep.UncommittedEvents, rep.UncommittedLine, rep.UncommittedStart, rep.UncommittedBatch,
				wantMembers, firstLine, wantEnd, b2)
		}
	}

	t.Run("excluded and counted", func(t *testing.T) {
		data := join(committed, members)
		events, end, rep := mustScan(t, data)
		wantLines(t, events, eventID(1), eventID(2), eventID(3))
		check(t, rep, end, int64(len(committed)), 3, 4)
		if rep.TornTail || rep.Lines != 6 || rep.Batches != 1 || rep.Size != int64(len(data)) {
			t.Errorf("report = %+v", rep)
		}
	})
	t.Run("a one-event batch", func(t *testing.T) {
		data := join(committed, enc(t, memberEvent(10, b2)))
		events, end, rep := mustScan(t, data)
		wantLines(t, events, eventID(1), eventID(2), eventID(3))
		check(t, rep, end, int64(len(committed)), 1, 4)
	})
	t.Run("a log that is only an uncommitted batch", func(t *testing.T) {
		events, end, rep := mustScan(t, members)
		if len(events) != 0 {
			t.Errorf("got %d events, want none", len(events))
		}
		check(t, rep, end, 0, 3, 1)
	})
	t.Run("a torn line after it reports both", func(t *testing.T) {
		torn := enc(t, memberEvent(13, b2))[:25]
		data := join(committed, members, torn)
		events, end, rep := mustScan(t, data)
		wantLines(t, events, eventID(1), eventID(2), eventID(3))
		check(t, rep, end, int64(len(committed)), 3, 4)
		tornStart := int64(len(committed) + len(members))
		if !rep.TornTail || rep.TornLine != 7 || rep.TornStart != tornStart || rep.TornBytes != int64(len(torn)) {
			t.Errorf("torn = {%v line %d start %d bytes %d}, want {true 7 %d %d}", rep.TornTail, rep.TornLine, rep.TornStart, rep.TornBytes, tornStart, len(torn))
		}
	})
	t.Run("a complete but unparsable line after it reports both", func(t *testing.T) {
		garbage := fixture(t, "garbage-line.jsonl")
		events, end, rep := mustScan(t, join(committed, members, garbage))
		wantLines(t, events, eventID(1), eventID(2), eventID(3))
		check(t, rep, end, int64(len(committed)), 3, 4)
		if !rep.TornTail || rep.TornLine != 7 {
			t.Errorf("torn = %v at line %d, want a torn tail at line 7", rep.TornTail, rep.TornLine)
		}
	})
	t.Run("a batch whose commit is torn is uncommitted", func(t *testing.T) {
		commit := enc(t, commitEvent(13, b2))
		data := join(committed, members, commit[:len(commit)-1])
		events, end, rep := mustScan(t, data)
		wantLines(t, events, eventID(1), eventID(2), eventID(3))
		check(t, rep, end, int64(len(committed)), 3, 4)
		if !rep.TornTail || rep.TornLine != 7 {
			t.Errorf("torn = %v at line %d, want a torn tail at line 7", rep.TornTail, rep.TornLine)
		}
	})
}

// INV-LOG-11: a torn or unparsable line, or an uncommitted batch, anywhere but
// the tail, an interleaved batch, a batch.committed with no open batch and a
// reused batch id are corruption. The line cited is the first line that proves
// it, so for a broken batch it is the line of the intruding event.
func TestScanCorruptionMidFile(t *testing.T) {
	b1, b2 := batchID(1), batchID(2)
	p := func(n int) []byte { return enc(t, plainEvent(n)) }
	m := func(n int, b event.ID) []byte { return enc(t, memberEvent(n, b)) }
	c := func(n int, b event.ID) []byte { return enc(t, commitEvent(n, b)) }
	cases := []struct {
		name string
		log  []byte
		line int
	}{
		{"garbage followed by a line", join(p(1), fixture(t, "garbage-line.jsonl"), p(2)), 2},
		{"a schema failure followed by a line", join(p(1), fixture(t, "schema-fail-line.jsonl"), p(2)), 2},
		{"a truncated line with a newline followed by a line", join(p(1), fixture(t, "truncated-line.jsonl"), p(2)), 2},
		{"a torn first line followed by a line", join(fixture(t, "garbage-line.jsonl"), p(1)), 1},
		{"an event.retracted carrying a batch followed by a line", join(p(1), p(2), fixture(t, "retracted-with-batch-line.jsonl"), p(3)), 3},
		{"an uncommitted batch followed by a plain event", join(p(1), m(2, b1), m(3, b1), p(4)), 4},
		{"an uncommitted batch followed by an event.retracted", join(p(1), m(2, b1), enc(t, retractBatchEvent(3, b1))), 3},
		{"an uncommitted batch followed by a batch of another id", join(p(1), m(2, b1), m(3, b2), c(4, b2)), 3},
		{"an interleaved batch", join(m(1, b1), m(2, b2), m(3, b1), c(4, b1), c(5, b2)), 2},
		{"a plain event inside a batch", join(m(1, b1), p(2), c(3, b1)), 2},
		{"batch.committed for another batch", join(m(1, b1), c(2, b2)), 2},
		{"batch.committed with no batch at all", join(p(1), c(2, b1)), 2},
		{"batch.committed twice for one batch", join(m(1, b1), c(2, b1), c(3, b1)), 3},
		{"a reused batch id", join(m(1, b1), c(2, b1), p(3), m(4, b1), c(5, b1)), 4},
		{"the first problem in file order wins", join(p(1), fixture(t, "garbage-line.jsonl"), fixture(t, "unknown-version-line.jsonl")), 2},
		{"a torn candidate wins over a later oversized line", join(p(1), fixture(t, "garbage-line.jsonl"), bytes.Repeat([]byte("a"), event.MaxEventBytes+1)), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, end, rep, err := scan(bytes.NewReader(tc.log))
			_ = wantCorrupt(t, err, tc.line)
			if events != nil || end != 0 || !reflect.DeepEqual(rep, scanReport{}) {
				t.Errorf("an error must come with zero results, got %d events, end %d, report %+v", len(events), end, rep)
			}
		})
	}

	t.Run("an event.retracted that names a batch is not a member of it", func(t *testing.T) {
		// INV-LOG-9: event.retracted carries no batch: its target_batch is not
		// membership, so it neither opens a batch, closes one, nor reuses an
		// id, whatever surrounds it.
		data := joinAll(
			batchLines(t, 1, 2, b1),
			[][]byte{enc(t, retractBatchEvent(4, b1)), enc(t, retractBatchEvent(5, b2))},
			batchLines(t, 6, 1, b2),
		)
		events, end, rep := mustScan(t, data)
		if len(events) != 7 || end != int64(len(data)) || rep.Batches != 2 || rep.UncommittedEvents != 0 {
			t.Errorf("got %d events, end %d of %d, report %+v; want a clean log of 7 events and 2 batches", len(events), end, len(data), rep)
		}
	})

	t.Run("the messages say what is wrong", func(t *testing.T) {
		_, _, _, err := scan(bytes.NewReader(join(p(1), c(2, b1))))
		if got := err.Error(); !strings.Contains(got, "line 2") || !strings.Contains(got, "no batch is open") || !strings.Contains(got, string(b1)) {
			t.Errorf("message %q should name line 2, the batch and that no batch is open", got)
		}
		_, _, _, err = scan(bytes.NewReader(join(p(1), fixture(t, "garbage-line.jsonl"), p(2))))
		if got := err.Error(); !strings.Contains(got, "line 2") || !strings.Contains(got, "more lines follow") {
			t.Errorf("message %q should name line 2 and say that more lines follow", got)
		}
	})
}

// INV-LOG-2: an event version this build does not know refuses the log, and is
// never taken for a torn tail.
func TestScanUnknownVersion(t *testing.T) {
	p := func(n int) []byte { return enc(t, plainEvent(n)) }
	v2 := fixture(t, "unknown-version-line.jsonl")
	cases := []struct {
		name string
		log  []byte
		line int
	}{
		{"the first line", join(v2, p(1)), 1},
		{"mid-file", join(p(1), p(2), v2, p(3)), 3},
		{"the final line", join(p(1), v2), 2},
		{"the final line without a newline", join(p(1), dropNewline(v2)), 2},
		{"the final line after an uncommitted batch", join(enc(t, memberEvent(1, batchID(1))), v2), 2},
		{"a lone line", v2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, end, rep, err := scan(bytes.NewReader(tc.log))
			var uv *UnknownVersionError
			if !errors.As(err, &uv) {
				t.Fatalf("scan error = %v (%T), want a *UnknownVersionError", err, err)
			}
			if uv.Line != tc.line || uv.V != 2 {
				t.Errorf("UnknownVersionError = {Line %d, V %d}, want {%d, 2}", uv.Line, uv.V, tc.line)
			}
			var inner *event.UnknownVersionError
			if !errors.As(err, &inner) || inner.V != 2 {
				t.Errorf("errors.As(*event.UnknownVersionError) = %v, %+v; want v 2", errors.As(err, &inner), inner)
			}
			var ce *CorruptError
			if errors.As(err, &ce) {
				t.Errorf("an unknown version must not be a CorruptError: %v", err)
			}
			if !strings.Contains(err.Error(), "line "+strconv.Itoa(tc.line)) || !strings.Contains(err.Error(), "version 2") {
				t.Errorf("message %q should name the line and the version", err)
			}
			if events != nil || end != 0 || !reflect.DeepEqual(rep, scanReport{}) {
				t.Errorf("an error must come with zero results")
			}
		})
	}

	t.Run("a version written another way is still unknown and keeps its text", func(t *testing.T) {
		line := bytes.Replace(v2, []byte(`"v":2`), []byte(`"v":1.0`), 1)
		_, _, _, err := scan(bytes.NewReader(join(p(1), line)))
		var inner *event.UnknownVersionError
		if !errors.As(err, &inner) || inner.Raw != "1.0" {
			t.Fatalf("scan error = %v, want an unknown version with Raw 1.0", err)
		}
		if !strings.Contains(err.Error(), "1.0") {
			t.Errorf("message %q should show the version as written", err)
		}
	})
	t.Run("a hand-built error still unwraps", func(t *testing.T) {
		err := error(&UnknownVersionError{Line: 4, V: 3})
		var inner *event.UnknownVersionError
		if !errors.As(err, &inner) || inner.V != 3 {
			t.Errorf("errors.As on a hand-built error = %v, %+v", errors.As(err, &inner), inner)
		}
	})
}

// INV-LOG-27: a 100 KiB line already in a log still replays, an event line is
// at most 262144 bytes, and INV-LOG-11: a longer line is corruption wherever
// it is.
func TestScanVeryLongLine(t *testing.T) {
	p := func(n int) []byte { return enc(t, plainEvent(n)) }

	t.Run("a 200 KiB note scans", func(t *testing.T) {
		note := strings.Repeat("n", 200*1024)
		data := join(p(1), enc(t, annotatedEvent(2, note)), p(3))
		events, end, rep := mustScan(t, data)
		wantLines(t, events, eventID(1), eventID(2), eventID(3))
		if end != int64(len(data)) || rep.TornTail {
			t.Errorf("end %d of %d, torn %v", end, len(data), rep.TornTail)
		}
		got := events[1].Payload.(event.CycleAnnotated).Note
		if got != note {
			t.Errorf("the note came back with %d bytes, want %d", len(got), len(note))
		}
	})
	t.Run("a line of exactly the limit scans", func(t *testing.T) {
		overhead := len(dropNewline(enc(t, annotatedEvent(1, "n")))) - 1 // the note key and its quotes
		line := enc(t, annotatedEvent(1, strings.Repeat("n", event.MaxEventBytes-overhead)))
		if got := len(dropNewline(line)); got != event.MaxEventBytes {
			t.Fatalf("the test line is %d bytes, want exactly %d", got, event.MaxEventBytes)
		}
		events, end, _ := mustScan(t, line)
		if len(events) != 1 || end != int64(len(line)) {
			t.Errorf("%d events, end %d of %d", len(events), end, len(line))
		}
	})

	over := bytes.Repeat([]byte("a"), event.MaxEventBytes+1)
	cases := []struct {
		name string
		log  []byte
		line int
	}{
		{"the first line", join(over, []byte("\n"), p(1)), 1},
		{"mid-file", join(p(1), over, []byte("\n"), p(2)), 2},
		{"the final line, newline-terminated", join(p(1), over, []byte("\n")), 2},
		{"the final line with no newline", join(p(1), over), 2},
		{"a long line that looks like an event", join(p(1), []byte(`{"v":1,"note":"`), bytes.Repeat([]byte("x"), event.MaxEventBytes), []byte("\"}\n")), 2},
	}
	for _, tc := range cases {
		t.Run("over the limit as "+tc.name, func(t *testing.T) {
			_, _, _, err := scan(bytes.NewReader(tc.log))
			ce := wantCorrupt(t, err, tc.line)
			if !errors.Is(ce, event.ErrTooLarge) {
				t.Errorf("cause = %v, want it to wrap event.ErrTooLarge", ce.Cause)
			}
			if !strings.Contains(err.Error(), "262144") {
				t.Errorf("message %q should state the limit", err)
			}
		})
	}
}

// endless reads the same byte forever and counts what was taken.
type endless struct{ taken int64 }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	e.taken += int64(len(p))
	return len(p), nil
}

// A huge line is refused after a bounded read: the scan neither buffers it nor
// reads it to its end (INV-LOG-11 and INV-LOG-27, with the memory bound the
// scan's comment promises).
func TestScanDoesNotBufferAHugeLine(t *testing.T) {
	r := &endless{}
	_, _, _, err := scan(r)
	_ = wantCorrupt(t, err, 1)
	if limit := int64(event.MaxEventBytes + 2*readBufferSize); r.taken > limit {
		t.Errorf("read %d bytes of an endless line, want at most %d", r.taken, limit)
	}
}

// INV-LOG-11: a blank line before the final line is corruption, and a blank
// final line is a torn tail (INV-LOG-9).
func TestScanBlankLine(t *testing.T) {
	p := func(n int) []byte { return enc(t, plainEvent(n)) }
	blank := []byte("\n")

	t.Run("before the final line is corrupt", func(t *testing.T) {
		_, _, _, err := scan(bytes.NewReader(join(p(1), blank, p(2))))
		ce := wantCorrupt(t, err, 2)
		if !strings.Contains(ce.Error(), "blank") {
			t.Errorf("message %q should say the line is blank", ce)
		}
	})
	t.Run("as the first line is corrupt", func(t *testing.T) {
		_, _, _, err := scan(bytes.NewReader(join(blank, p(1))))
		_ = wantCorrupt(t, err, 1)
	})
	t.Run("a line of only white space before the final line is corrupt", func(t *testing.T) {
		_, _, _, err := scan(bytes.NewReader(join(p(1), []byte("  \t\n"), p(2))))
		_ = wantCorrupt(t, err, 2)
	})
	t.Run("two blank lines are corrupt at the first", func(t *testing.T) {
		_, _, _, err := scan(bytes.NewReader(join(p(1), blank, blank)))
		_ = wantCorrupt(t, err, 2)
	})
	t.Run("as the final line is a torn tail", func(t *testing.T) {
		committed := join(p(1), p(2))
		events, end, rep := mustScan(t, join(committed, blank))
		wantLines(t, events, eventID(1), eventID(2))
		if end != int64(len(committed)) || !rep.TornTail || rep.TornLine != 3 || rep.TornStart != int64(len(committed)) || rep.TornBytes != 1 {
			t.Errorf("end %d, report %+v; want a torn tail of one byte at line 3, offset %d", end, rep, len(committed))
		}
	})
	t.Run("a log of one blank line is a torn tail", func(t *testing.T) {
		events, end, rep := mustScan(t, blank)
		if len(events) != 0 || end != 0 || !rep.TornTail || rep.TornLine != 1 {
			t.Errorf("%d events, end %d, report %+v", len(events), end, rep)
		}
	})
	t.Run("blank space with no newline is a torn tail", func(t *testing.T) {
		committed := p(1)
		events, end, rep := mustScan(t, join(committed, []byte("   ")))
		wantLines(t, events, eventID(1))
		if end != int64(len(committed)) || !rep.TornTail || rep.TornBytes != 3 {
			t.Errorf("end %d, report %+v", end, rep)
		}
	})
}

// failing returns its data and then a read error.
type failing struct {
	data []byte
	err  error
}

func (f *failing) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

// A read failure is not a statement about the log: it is neither corruption
// nor a torn tail, whatever was read before it.
func TestScanReadFailureIsNotCorruption(t *testing.T) {
	boom := errors.New("disk on fire")
	for name, data := range map[string][]byte{
		"after a clean line":      enc(t, plainEvent(1)),
		"after an unparsable":     join(enc(t, plainEvent(1)), fixture(t, "garbage-line.jsonl")),
		"in the middle of a line": enc(t, plainEvent(1))[:30],
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := scan(&failing{data: data, err: boom})
			var ce *CorruptError
			if !errors.Is(err, boom) || errors.As(err, &ce) {
				t.Errorf("scan error = %v, want the read error and no CorruptError", err)
			}
		})
	}
}
