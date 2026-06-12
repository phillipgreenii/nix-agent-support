package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
