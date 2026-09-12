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
	if err := w.Emit("info", "reminder", "near limit", map[string]any{"session": "s", "pct": 0.73}); err != nil {
		t.Fatal(err)
	}
	if err := w.Emit("error", "hard_stop", "budget hard stop reached", map[string]any{"session": "s", "bead": "zr-1"}); err != nil {
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
		for _, k := range []string{"time", "level", "msg", "kind"} {
			if m[k] == nil {
				t.Errorf("line %d missing %q", n, k)
			}
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

func TestEmit_stampsStandardFields(t *testing.T) {
	fixed := time.Date(2026, 6, 12, 15, 4, 5, 123456789, time.UTC)
	want := fixed.UTC().Format(time.RFC3339Nano)

	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.Now = func() time.Time { return fixed }

	if err := w.Emit("info", "reminder", "budget reminder threshold reached",
		map[string]any{"session": "s", "bead": "zr-1"}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rec := readFirstRecord(t, p)
	if rec["time"] != want {
		t.Errorf("time = %v, want %v", rec["time"], want)
	}
	if rec["level"] != "info" {
		t.Errorf("level = %v, want info", rec["level"])
	}
	if rec["msg"] != "budget reminder threshold reached" {
		t.Errorf("msg = %v, want the reminder message", rec["msg"])
	}
	if rec["kind"] != "reminder" {
		t.Errorf("kind = %v, want reminder", rec["kind"])
	}
	if _, ok := rec["ts"]; ok {
		t.Errorf("legacy ts field must be gone, got %v", rec["ts"])
	}
	if rec["session"] != "s" || rec["bead"] != "zr-1" {
		t.Errorf("caller fields dropped: %v", rec)
	}
}

func TestEmit_reservedKeysNotOverridable(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := fixed.UTC().Format(time.RFC3339Nano)

	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := New(p)
	defer func() { _ = w.Close() }()
	w.Now = func() time.Time { return fixed }

	// Caller tries to override every stamped key; all must be ignored.
	if err := w.Emit("warn", "real_kind", "real message", map[string]any{
		"time":  "1999-12-31T23:59:59Z",
		"level": "debug",
		"kind":  "fake_kind",
		"msg":   "fake message",
		"keep":  "me",
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rec := readFirstRecord(t, p)
	if rec["time"] != want {
		t.Errorf("caller overrode time: %v", rec["time"])
	}
	if rec["level"] != "warn" {
		t.Errorf("caller overrode level: %v", rec["level"])
	}
	if rec["kind"] != "real_kind" {
		t.Errorf("caller overrode kind: %v", rec["kind"])
	}
	if rec["msg"] != "real message" {
		t.Errorf("caller overrode msg: %v", rec["msg"])
	}
	if rec["keep"] != "me" {
		t.Errorf("non-reserved field dropped: %v", rec)
	}
}

func TestEmit_concurrentNoInterleave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := New(p)
	defer func() { _ = w.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = w.Emit("debug", "tick", "t", map[string]any{"i": i}) }(i)
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
