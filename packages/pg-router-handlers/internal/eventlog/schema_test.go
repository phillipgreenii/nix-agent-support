package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// validLevels mirrors the JSONL standard enum
// (phillipgreenii-nix-support-apps/docs/schemas/jsonl-log.schema.json).
var validLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

func TestEmit_conformsToJSONLStandard(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Emit("info", "reminder", "budget reminder threshold reached", map[string]any{"bead": "zr-1"})
	_ = w.Emit("warn", "cancel", "budget cancel threshold reached", map[string]any{"bead": "zr-1"})
	_ = w.Close()

	f, _ := os.Open(p)
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		lines++
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %d invalid JSON: %v", lines, err)
		}
		for _, k := range []string{"time", "level", "msg"} {
			if _, ok := m[k]; !ok {
				t.Errorf("line %d missing required field %q: %v", lines, k, m)
			}
		}
		if lvl, _ := m["level"].(string); !validLevels[lvl] {
			t.Errorf("line %d level %q not in standard enum", lines, lvl)
		}
		if ts, _ := m["time"].(string); ts == "" {
			t.Errorf("line %d empty time", lines)
		} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("line %d time %q not RFC3339: %v", lines, ts, err)
		}
		if _, ok := m["kind"]; !ok {
			t.Errorf("line %d dropped the kind field: %v", lines, m)
		}
	}
	if lines != 2 {
		t.Fatalf("want 2 lines, got %d", lines)
	}
}
