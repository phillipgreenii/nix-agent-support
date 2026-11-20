package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

func seedDeadLetter(t *testing.T, path, id string) {
	t.Helper()
	evt := eventqueue.Event{ID: id, Type: "T", Payload: map[string]any{}}
	dl := eventqueue.DeadLetter{
		EventID: id, Role: "r", Class: "deterministic", Attempts: 1, Reason: "deterministic",
		At: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	if err := eventqueue.RecordSettledFailure(path, evt, dl); err != nil {
		t.Fatal(err)
	}
}

func TestDeadLettersJSONListsHeldRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	seedDeadLetter(t, path, "e1")
	var out, errb bytes.Buffer
	if code := deadLetters(&out, &errb, path, true); code != exitOK {
		t.Fatalf("exit = %d (%s)", code, errb.String())
	}
	var got struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"eventId": "e1", "role": "r", "class": "deterministic", "attempts": float64(1),
		"reason": "deterministic", "at": "2026-11-20T10:00:00Z",
	}
	if len(got.Records) != 1 || len(got.Records[0]) != 6 {
		t.Fatalf("records = %+v, want one with six fields", got.Records)
	}
	for k, v := range want {
		if got.Records[0][k] != v {
			t.Fatalf("field %s = %v, want %v", k, got.Records[0][k], v)
		}
	}
}

func TestDeadLettersHumanOneLinePerRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	seedDeadLetter(t, path, "e1")
	var out, errb bytes.Buffer
	if code := deadLetters(&out, &errb, path, false); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "e1 role=r class=deterministic") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDeadLettersMissingLogIsEmptyExitZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	var out, errb bytes.Buffer
	if code := deadLetters(&out, &errb, path, true); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != `{"records":[]}` {
		t.Fatalf("output = %q, want an empty records array", got)
	}
	out.Reset()
	if code := deadLetters(&out, &errb, path, false); code != exitOK || !strings.Contains(out.String(), "no dead letters") {
		t.Fatalf("human mode: exit %d output %q", code, out.String())
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the read-only verb must not create the log")
	}
}

func TestDeadLettersWorksWhileLogLockedAndIgnoresTornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	seedDeadLetter(t, path, "e1")
	fs, err := eventqueue.NewFileStore(path) // a daemon's hold on the log lock
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"op":"failed","eventId":"e1","listen`)
	_ = f.Close()
	var out, errb bytes.Buffer
	if code := deadLetters(&out, &errb, path, true); code != exitOK || !strings.Contains(out.String(), `"eventId":"e1"`) {
		t.Fatalf("exit %d output %q err %q", code, out.String(), errb.String())
	}
}

func TestDeadLettersUnreadableLogExits3(t *testing.T) {
	var out, errb bytes.Buffer
	if code := deadLetters(&out, &errb, t.TempDir(), true); code != 3 {
		t.Fatalf("exit = %d, want 3 (unavailable)", code)
	}
}

func TestDeadLettersBadArgumentsExit2(t *testing.T) {
	if code := runDeadLetters([]string{"--bogus"}); code != 2 {
		t.Fatalf("unknown flag exit = %d, want 2", code)
	}
	if code := runDeadLetters([]string{"extra"}); code != 2 {
		t.Fatalf("positional exit = %d, want 2", code)
	}
}

func TestRouteDeadLetters(t *testing.T) {
	if r := route([]string{"pg-router", "dead-letters", "--json"}); r.kind != routeDeadLetters || len(r.rest) != 1 || r.rest[0] != "--json" {
		t.Fatalf("route(dead-letters --json) = %+v", r)
	}
	if !strings.Contains(usageLine, "dead-letters [--json]") || !strings.Contains(helpText, "dead-letters [--json]") {
		t.Fatal("usage and help must advertise dead-letters [--json]")
	}
}

// A failed one-shot run-role dispatch leaves one dead letter in the log; with the
// log locked by a daemon it writes nothing and does not panic.
func TestRecordRunRoleFailure(t *testing.T) {
	dir := t.TempDir()
	evt := eventqueue.Event{ID: "e1", Type: "T", Payload: map[string]any{}}
	recordRunRoleFailure(dir, evt, "r", &wireclient.ExitError{Role: "r", Code: 1, Detail: "invalid_argument: x"})
	got, err := eventqueue.ReadDeadLettersFile(filepath.Join(dir, "queue.jsonl"))
	if err != nil || len(got) != 1 || got[0].Class != "deterministic" || got[0].Role != "r" || got[0].Attempts != 1 || got[0].Reason != "deterministic" {
		t.Fatalf("dead letters = %+v, %v", got, err)
	}
	fs, err := eventqueue.NewFileStore(filepath.Join(dir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	recordRunRoleFailure(dir, evt, "r", &wireclient.ExitError{Role: "r", Code: 1}) // locked: only a stderr note
}
