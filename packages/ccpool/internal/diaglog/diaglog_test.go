package diaglog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLog_writesLowercaseTimeLevelMsgJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.jsonl")
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := time.Date(2026, 6, 23, 14, 30, 0, 0, time.UTC)
	if err := lg.Log(ts, "error", "hook stop: store open: boom"); err != nil {
		t.Fatalf("Log: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Decode into a generic map so we assert the EXACT lowercase keys/values
	// the otelcol json_parser + severity_parser depend on.
	var got map[string]any
	if err := json.Unmarshal(b[:len(b)-1], &got); err != nil { // strip trailing \n
		t.Fatalf("unmarshal %q: %v", b, err)
	}
	if got["time"] != "2026-06-23T14:30:00Z" {
		t.Errorf("time = %v, want RFC3339 UTC 2026-06-23T14:30:00Z", got["time"])
	}
	if got["level"] != "error" {
		t.Errorf("level = %v, want error", got["level"])
	}
	if got["msg"] != "hook stop: store open: boom" {
		t.Errorf("msg = %v, want the diagnostic text", got["msg"])
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Errorf("line must end in a newline (JSONL): %q", b)
	}
}

func TestLog_appendsOneLinePerCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.jsonl")
	lg, _ := Open(path)
	ts := time.Unix(0, 0).UTC()
	_ = lg.Log(ts, "error", "first")
	_ = lg.Log(ts, "warn", "second")
	b, _ := os.ReadFile(path)
	lines := 0
	for _, c := range b {
		if c == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("got %d lines, want 2 (one JSON object per Log call)", lines)
	}
}

func TestNilLogger_isNoOp(t *testing.T) {
	var lg *Logger // nil
	if err := lg.Log(time.Now(), "error", "ignored"); err != nil {
		t.Errorf("nil Logger.Log must be a no-op returning nil, got %v", err)
	}
}
