package store

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// genLog is a generated log: its bytes, the committed part of it and what its
// tail leaves unacknowledged.
type genLog struct {
	data      []byte
	committed []byte   // the serialized committed prefix
	lines     [][]byte // the committed lines, newline-terminated
	batches   int
	// uncommitted is the number of events of the trailing open batch.
	uncommitted int
	torn        bool
}

// drawLog draws a random sequence of plain events, committed batches and
// retractions of committed batches, then an optional tail: an uncommitted
// batch, a torn line (a prefix with or without its newline), or both.
func drawLog(t *rapid.T) genLog {
	var g genLog
	n := 0
	next := func() int { n++; return n }
	var batches []event.ID
	add := func(line []byte) {
		g.lines = append(g.lines, line)
	}

	for range rapid.IntRange(0, 10).Draw(t, "units") {
		switch rapid.IntRange(0, 3).Draw(t, "unit") {
		case 0, 1:
			add(enc(t, plainEvent(next())))
		case 2:
			b := batchID(len(batches) + 1)
			batches = append(batches, b)
			for range rapid.IntRange(1, 4).Draw(t, "members") {
				add(enc(t, memberEvent(next(), b)))
			}
			add(enc(t, commitEvent(next(), b)))
			g.batches++
		case 3:
			if len(batches) == 0 {
				add(enc(t, plainEvent(next())))
				break
			}
			b := rapid.SampledFrom(batches).Draw(t, "retracted batch")
			add(enc(t, retractBatchEvent(next(), b)))
		}
	}
	g.committed = join(g.lines...)
	g.data = g.committed

	tail := rapid.SampledFrom([]string{"none", "uncommitted", "torn", "torn newline", "uncommitted torn"}).Draw(t, "tail")
	if tail == "uncommitted" || tail == "uncommitted torn" {
		b := batchID(len(batches) + 1)
		g.uncommitted = rapid.IntRange(1, 3).Draw(t, "uncommitted members")
		for range g.uncommitted {
			g.data = join(g.data, enc(t, memberEvent(next(), b)))
		}
	}
	if tail == "torn" || tail == "torn newline" || tail == "uncommitted torn" {
		victim := dropNewline(enc(t, rapid.SampledFrom([]event.Event{
			plainEvent(next()), memberEvent(next(), batchID(99)), commitEvent(next(), batchID(99)),
		}).Draw(t, "victim")))
		if tail == "torn newline" {
			// A strict prefix with its newline is complete but cannot decode.
			g.data = join(g.data, victim[:rapid.IntRange(0, len(victim)-1).Draw(t, "cut")], []byte("\n"))
		} else {
			// Any non-empty prefix, up to the whole line without its newline.
			g.data = join(g.data, victim[:rapid.IntRange(1, len(victim)).Draw(t, "cut")])
		}
		g.torn = true
	}
	return g
}

// Property: scan returns exactly the committed events and the offset of their
// end whatever tail the log has, and recovering (truncating at that offset)
// is idempotent (INV-LOG-9, INV-LOG-10 and INV-LOG-11).
func TestScanProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := drawLog(t)
		events, end, rep, err := scan(bytes.NewReader(g.data))
		if err != nil {
			t.Fatalf("scan: %v", err)
		}

		// (a) exactly the committed events, as written.
		if len(events) != len(g.lines) {
			t.Fatalf("scan returned %d events, want %d", len(events), len(g.lines))
		}
		for i, e := range events {
			line, err := event.Encode(e)
			if err != nil {
				t.Fatalf("re-encoding event %d: %v", i, err)
			}
			if !bytes.Equal(append(line, '\n'), g.lines[i]) {
				t.Fatalf("event %d is not the line written:\n got %s\nwant %s", i, line, g.lines[i])
			}
			if e.Line != i+1 {
				t.Fatalf("event %d has Line %d", i, e.Line)
			}
		}

		// (b) the end of the committed prefix.
		if end != int64(len(g.committed)) {
			t.Fatalf("endOfLastCommitted = %d, want %d", end, len(g.committed))
		}
		if rep.TornTail != g.torn || rep.UncommittedEvents != g.uncommitted || rep.Batches != g.batches || rep.Size != int64(len(g.data)) {
			t.Fatalf("report %+v, want torn %v, %d uncommitted, %d batches, size %d", rep, g.torn, g.uncommitted, g.batches, len(g.data))
		}
		if g.torn && rep.TornStart+rep.TornBytes != rep.Size {
			t.Fatalf("the torn range [%d, +%d) does not end at the end of the input, %d", rep.TornStart, rep.TornBytes, rep.Size)
		}
		if g.uncommitted > 0 && rep.UncommittedStart != end {
			t.Fatalf("the uncommitted batch starts at %d, want the end of the committed prefix, %d", rep.UncommittedStart, end)
		}

		// (c) recovery idempotence.
		assertRecoversCleanly(t, g.data[:end], events)
	})
}

// assertRecoversCleanly scans a truncated log and expects the same events and
// nothing left over.
func assertRecoversCleanly(t *rapid.T, truncated []byte, want []event.Event) {
	t.Helper()
	again, end, rep, err := scan(bytes.NewReader(truncated))
	if err != nil {
		t.Fatalf("scan of the recovered log: %v", err)
	}
	if end != int64(len(truncated)) || rep.TornTail || rep.UncommittedEvents != 0 {
		t.Fatalf("the recovered log is not clean: end %d of %d, report %+v", end, len(truncated), rep)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("the recovered log scans to different events")
	}
}

// Property: scan never panics, and on any bytes it either returns one of its
// two typed errors or a result that recovers cleanly.
func TestScanNeverPanicsOnArbitraryBytes(t *testing.T) {
	check := func(t *rapid.T, data []byte) {
		events, end, _, err := scan(bytes.NewReader(data))
		if err != nil {
			var ce *CorruptError
			var uv *UnknownVersionError
			if !errors.As(err, &ce) && !errors.As(err, &uv) {
				t.Fatalf("scan error %v (%T) is neither corruption nor an unknown version", err, err)
			}
			return
		}
		if end < 0 || end > int64(len(data)) {
			t.Fatalf("endOfLastCommitted %d is outside the input of %d bytes", end, len(data))
		}
		assertRecoversCleanly(t, data[:end], events)
	}

	t.Run("raw bytes", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			check(t, rapid.SliceOf(rapid.Byte()).Draw(t, "bytes"))
		})
	})
	t.Run("a valid log with bytes flipped, dropped and inserted", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			data := append([]byte(nil), drawLog(t).data...)
			for range rapid.IntRange(1, 4).Draw(t, "edits") {
				if len(data) == 0 {
					break
				}
				i := rapid.IntRange(0, len(data)-1).Draw(t, "at")
				switch rapid.IntRange(0, 2).Draw(t, "edit") {
				case 0:
					data[i] = rapid.Byte().Draw(t, "byte")
				case 1:
					data = append(data[:i], data[i+1:]...)
				case 2:
					data = append(data[:i], append([]byte{rapid.SampledFrom([]byte("\n\"{},:0 ")).Draw(t, "inserted")}, data[i:]...)...)
				}
			}
			check(t, data)
		})
	})
}
