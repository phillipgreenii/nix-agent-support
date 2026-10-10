package wire_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// The notes format of a calendar event, with its reference parser: the
// conformance test the emitter and the parser share.
func TestNotesRoundTripThroughTheReferenceParser(t *testing.T) {
	for name, c := range map[string]struct {
		kv   []event.KV
		note string
	}{
		"no pairs, no note":    {nil, ""},
		"pairs and a note":     {[]event.KV{{Key: "ticket", Value: "ABC-1"}, {Key: "ticket", Value: "ABC-2"}, {Key: "pr", Value: "1234"}}, "Reviewed the migration."},
		"a note holding ---":   {[]event.KV{{Key: "pr", Value: "7"}}, "first\n---\nsecond"},
		"a multi-line value":   {[]event.KV{{Key: "ticket", Value: "A\nB\tC  "}}, "n"},
		"an empty note and kv": {[]event.KV{{Key: "k", Value: ""}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			text := wire.Notes("deep-work", c.kv, c.note)
			if !strings.HasPrefix(text, "cycle_type: deep-work\n") {
				t.Fatalf("the reserved pair is not first: %q", text)
			}
			if !strings.Contains(text, "\n---\n") {
				t.Fatalf("the --- line is always present: %q", text)
			}
			pairs, note, ok := wire.ParseNotes(text)
			if !ok {
				t.Fatalf("no separator: %q", text)
			}
			if pairs[0].Key != "cycle_type" || pairs[0].Value != "deep-work" {
				t.Errorf("first pair = %+v", pairs[0])
			}
			if len(pairs)-1 != len(c.kv) {
				t.Fatalf("%d operator pairs, want %d: %+v", len(pairs)-1, len(c.kv), pairs)
			}
			for i, p := range c.kv {
				got := pairs[i+1]
				want := strings.TrimSpace(strings.Map(func(r rune) rune {
					if r == '\n' || r == '\t' {
						return ' '
					}
					return r
				}, p.Value))
				if got.Key != p.Key || got.Value != want {
					t.Errorf("pair %d = %+v, want %s: %q", i, got, p.Key, want)
				}
			}
			if note != c.note {
				t.Errorf("note = %q, want %q", note, c.note)
			}
		})
	}
	if _, _, ok := wire.ParseNotes("cycle_type: x\nk: v\n"); ok {
		t.Error("a document with no --- line parsed")
	}
}

func TestBuildCalendarClipsAndOrdersSegments(t *testing.T) {
	base := time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)
	end1 := base.Add(30 * time.Minute)
	cycles := []projection.Cycle{{
		ID: "C1", Type: "deep-work", Title: "Deep work cycle", Note: "n",
		Segments: []projection.Segment{
			{Start: base, End: &end1, OpenedBy: "E1"},
			{Start: base.Add(time.Hour), OpenedBy: "E2"},                                        // open: ends at the read time
			{Start: base.Add(2 * time.Hour), End: ptr(base.Add(2 * time.Hour)), OpenedBy: "E3"}, // zero length: never emitted
		},
	}}
	now := base.Add(90 * time.Minute)
	cal := wire.BuildCalendar(cycles, base.Add(-time.Hour), base.Add(24*time.Hour), now)
	if len(cal.Events) != 2 || cal.Events[0].ID != "E1" || cal.Events[1].ID != "E2" {
		t.Fatalf("events = %+v", cal.Events)
	}
	if got := cal.Events[1].End.Time(); !got.Equal(now) {
		t.Errorf("the open segment ends at %v, want the read time %v", got, now)
	}
	// [from, to) overlap: a window that ends exactly where the first segment starts excludes it.
	if got := wire.BuildCalendar(cycles, base.Add(-time.Hour), base, now); len(got.Events) != 0 {
		t.Errorf("a window ending at a segment's start included it: %+v", got.Events)
	}
	if cal.Stale || cal.CalendarID != "focus-cycles" {
		t.Errorf("calendar = %+v", cal)
	}
}

func ptr[T any](v T) *T { return &v }

func TestProblemOfCarriesTheStructuredDetails(t *testing.T) {
	r := &command.Rejection{
		Reason: command.ReasonCycleAmbiguous, Message: "Two cycles qualify.", Entity: "",
		Events: []event.ID{"E1"}, Instants: []time.Time{time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)},
		Cycles: []command.CycleRef{{ID: "C1", Title: "Deep work cycle", Status: projection.Paused}},
	}
	p := wire.ProblemOf(r, "/api/v1/cycles/stop", "trace", nil)
	b, _ := json.Marshal(p)
	for _, want := range []string{`"reason":"cycle_ambiguous"`, `"status":400`, `"trace_id":"trace"`, `"id":"C1"`, `"2026-10-07T13:00:00.000Z"`, `"type":"urn:pg-task-focus:problem:cycle_ambiguous"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("problem %s lacks %s", b, want)
		}
	}
	// Every library reason has a status, and the transport reasons have theirs.
	for _, reason := range command.Reasons() {
		if s := wire.StatusOf(reason); s < 400 || s > 503 {
			t.Errorf("reason %s has status %d", reason, s)
		}
	}
	for _, reason := range wire.TransportReasons() {
		if s := wire.StatusOf(reason); s < 400 {
			t.Errorf("transport reason %s has status %d", reason, s)
		}
	}
	if wire.StatusOf("nonsense") != 500 {
		t.Error("an unknown reason is a 500")
	}
	// The READ-ONLY sentence is the one every client shows.
	since := event.At(time.Now())
	st := wire.Store{State: wire.StoreReadOnly, Reason: "the append fsync failed", Since: &since}
	if got := st.ReadOnlySentence(); got != "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover" {
		t.Errorf("sentence = %q", got)
	}
	if (wire.Store{State: wire.StoreOK}).ReadOnlySentence() != "" {
		t.Error("a healthy store has no sentence")
	}
}

func TestSortReasonsIsStableAndComplete(t *testing.T) {
	got := wire.SortReasons(command.Reasons())
	if len(got) != len(command.Reasons()) {
		t.Fatalf("%d reasons, want %d", len(got), len(command.Reasons()))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("not sorted at %d: %s, %s", i, got[i-1], got[i])
		}
	}
}

func TestTimeInputReadsAnyOffsetAsAnInstant(t *testing.T) {
	var in struct{ At wire.Time }
	if err := json.Unmarshal([]byte(`{"At":"2026-10-07T09:30:00-04:00"}`), &in); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC); !in.At.Time.Equal(want) {
		t.Errorf("At = %v, want %v", in.At.Time, want)
	}
	for _, bad := range []string{`{"At":"yesterday"}`, `{"At":5}`, `{"At":"2026-10-07"}`} {
		if err := json.Unmarshal([]byte(bad), &in); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
