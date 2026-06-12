package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEmit_writesJSONLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if err := w.Emit("reminder", map[string]any{"session": "s", "pct": 0.73}); err != nil {
		t.Fatal(err)
	}
	if err := w.Emit("hard_stop", map[string]any{"session": "s", "bead": "zr-1"}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer func() { _ = f.Close() }()
	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %d not valid JSON: %v", n, err)
		}
		if m["kind"] == nil {
			t.Errorf("line %d missing kind", n)
		}
		n++
	}
	if n != 2 {
		t.Errorf("want 2 lines, got %d", n)
	}
}

// readFirstRecord opens the JSONL file at p and unmarshals its first line.
func readFirstRecord(t *testing.T, p string) map[string]any {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("no lines in %s", p)
	}
	var m map[string]any
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", sc.Text(), err)
	}
	return m
}

func TestEmit_stampsTimestamp(t *testing.T) {
	fixed := time.Date(2026, 6, 12, 15, 4, 5, 123456789, time.UTC)
	want := fixed.UTC().Format(time.RFC3339Nano)

	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.Now = func() time.Time { return fixed }

	if err := w.Emit("reminder", map[string]any{"session": "s", "bead": "zr-1"}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rec := readFirstRecord(t, p)
	if rec["ts"] != want {
		t.Errorf("ts = %v, want %v", rec["ts"], want)
	}
	if rec["kind"] != "reminder" {
		t.Errorf("kind = %v, want reminder", rec["kind"])
	}
	if rec["session"] != "s" {
		t.Errorf("session = %v, want s", rec["session"])
	}
	if rec["bead"] != "zr-1" {
		t.Errorf("bead = %v, want zr-1", rec["bead"])
	}
}

func TestEmit_callerTsAndKindDoNotOverride(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := fixed.UTC().Format(time.RFC3339Nano)

	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.Now = func() time.Time { return fixed }

	// Caller tries to override the stamped ts and kind; both must be ignored.
	if err := w.Emit("real_kind", map[string]any{
		"ts":      "1999-12-31T23:59:59Z",
		"kind":    "fake_kind",
		"session": "s",
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rec := readFirstRecord(t, p)
	if rec["ts"] != want {
		t.Errorf("caller ts overrode stamp: ts = %v, want %v", rec["ts"], want)
	}
	if rec["kind"] != "real_kind" {
		t.Errorf("caller kind overrode kind arg: kind = %v, want real_kind", rec["kind"])
	}
	if rec["session"] != "s" {
		t.Errorf("session = %v, want s", rec["session"])
	}
}

func TestEmit_concurrentNoInterleave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := New(p)
	defer func() { _ = w.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = w.Emit("tick", map[string]any{"i": i}) }(i)
	}
	wg.Wait()
	f, _ := os.Open(p)
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("interleaved/corrupt line: %v", err)
		}
		n++
	}
	if n != 50 {
		t.Errorf("want 50 lines, got %d", n)
	}
}
