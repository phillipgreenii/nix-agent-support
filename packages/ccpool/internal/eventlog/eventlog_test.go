package eventlog

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendReadRoundTrip_preservesOrderAndFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ts := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// A mixed, ordered sequence: transition, input, transition, input.
	if err := lg.Transition(ts, "alpha", "starting", "ready", "u-alpha", "/p/a.jsonl"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := lg.Input(ts.Add(time.Second), "alpha", "clear-input", "C-u"); err != nil {
		t.Fatalf("Input clear: %v", err)
	}
	if err := lg.Input(ts.Add(2*time.Second), "alpha", "paste", "prompt body delivered"); err != nil {
		t.Fatalf("Input paste: %v", err)
	}
	if err := lg.Transition(ts.Add(3*time.Second), "alpha", "ready", "working", "", ""); err != nil {
		t.Fatalf("Transition2: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Read returned %d events, want 4: %+v", len(got), got)
	}

	// Order + fields.
	if got[0].Kind != "transition" || got[0].Name != "alpha" ||
		got[0].From != "starting" || got[0].To != "ready" ||
		got[0].UUID != "u-alpha" || got[0].LineRef != "/p/a.jsonl" {
		t.Errorf("event[0] = %+v", got[0])
	}
	if got[0].Ts != "2026-06-15T12:00:00Z" {
		t.Errorf("event[0].Ts = %q, want RFC3339Nano UTC", got[0].Ts)
	}
	if got[1].Kind != "input" || got[1].Action != "clear-input" || got[1].Detail != "C-u" {
		t.Errorf("event[1] = %+v", got[1])
	}
	if got[2].Kind != "input" || got[2].Action != "paste" {
		t.Errorf("event[2] = %+v", got[2])
	}
	if got[3].Kind != "transition" || got[3].From != "ready" || got[3].To != "working" {
		t.Errorf("event[3] = %+v", got[3])
	}
	// omitempty: a transition with no uuid/line_ref must not carry input fields.
	if got[3].Action != "" || got[3].Detail != "" {
		t.Errorf("transition event leaked input fields: %+v", got[3])
	}
}

func TestRead_missingFile_isEmptyNoError(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("Read of missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Read of missing file = %+v, want empty", got)
	}
}

func TestNilLogger_methodsAreNoOps(t *testing.T) {
	var lg *Logger // nil
	ts := time.Now()
	if err := lg.Append(Event{}); err != nil {
		t.Errorf("nil Append err = %v, want nil", err)
	}
	if err := lg.Transition(ts, "a", "x", "y", "u", "ref"); err != nil {
		t.Errorf("nil Transition err = %v, want nil", err)
	}
	if err := lg.Input(ts, "a", "enter", ""); err != nil {
		t.Errorf("nil Input err = %v, want nil", err)
	}
}
