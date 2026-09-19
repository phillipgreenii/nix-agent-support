package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAttributionLoggerWritesOneJSONLinePerEntry covers B1's concrete
// attribution log format (ADR 0071 §4 Phase B, B1 "Attribution log"): one
// JSON object per line, with exactly the documented keys.
func TestAttributionLoggerWritesOneJSONLinePerEntry(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewAttributionLogger(dir)
	if err != nil {
		t.Fatalf("NewAttributionLogger() error = %v", err)
	}

	entry := AttributionEntry{
		Timestamp:     time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		HookEventName: "PreToolUse",
		DelegateName:  "example-delegate",
		Contract:      "decide",
		Verdict:       VerdictApplied,
		DurationMS:    42,
	}
	if err := logger.Log(entry); err != nil {
		t.Fatalf("Log() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "attribution.jsonl"))
	if err != nil {
		t.Fatalf("read attribution log: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("attribution log line is not valid JSON: %v (line: %q)", err, data)
	}

	for _, key := range []string{"timestamp", "hook_event_name", "delegate_name", "contract", "verdict", "duration_ms"} {
		if _, ok := got[key]; !ok {
			t.Errorf("attribution log entry missing key %q: %v", key, got)
		}
	}
	if got["hook_event_name"] != "PreToolUse" {
		t.Errorf("hook_event_name = %v, want PreToolUse", got["hook_event_name"])
	}
	if got["delegate_name"] != "example-delegate" {
		t.Errorf("delegate_name = %v, want example-delegate", got["delegate_name"])
	}
	if got["contract"] != "decide" {
		t.Errorf("contract = %v, want decide", got["contract"])
	}
	if got["verdict"] != "applied" {
		t.Errorf("verdict = %v, want applied", got["verdict"])
	}
	if got["duration_ms"].(float64) != 42 {
		t.Errorf("duration_ms = %v, want 42", got["duration_ms"])
	}
}

// TestAttributionLoggerAppendsMultipleEntriesAsSeparateLines covers the
// "JSON-lines" shape end to end: two Log() calls produce two parseable
// lines, in call order.
func TestAttributionLoggerAppendsMultipleEntriesAsSeparateLines(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewAttributionLogger(dir)
	if err != nil {
		t.Fatalf("NewAttributionLogger() error = %v", err)
	}

	if err := logger.Log(AttributionEntry{DelegateName: "one", Verdict: VerdictApplied}); err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if err := logger.Log(AttributionEntry{DelegateName: "two", Verdict: VerdictAbstain}); err != nil {
		t.Fatalf("Log() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "attribution.jsonl"))
	if err != nil {
		t.Fatalf("read attribution log: %v", err)
	}

	lines := splitNonEmptyLines(string(data))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), data)
	}
	var first, second struct {
		DelegateName string `json:"delegate_name"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 1 not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("line 2 not valid JSON: %v", err)
	}
	if first.DelegateName != "one" || second.DelegateName != "two" {
		t.Errorf("got delegate order (%q, %q), want (\"one\", \"two\")", first.DelegateName, second.DelegateName)
	}
}

func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if line := s[start:i]; line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// TestAttributionLoggerNilIsASafeNoOp covers dispatch()'s own contract: when
// CLAUDE_PLUGIN_DATA is unavailable and no logger could be constructed, main
// still calls Log() unconditionally on a nil *AttributionLogger, which must
// never panic.
func TestAttributionLoggerNilIsASafeNoOp(t *testing.T) {
	var logger *AttributionLogger
	if err := logger.Log(AttributionEntry{DelegateName: "x"}); err != nil {
		t.Errorf("nil logger Log() error = %v, want nil", err)
	}
}

// TestAttributionLoggerRotatesPastSizeThreshold covers the single-generation
// rotation: once the log file is already at or past maxAttributionLogBytes,
// the next Log() call renames it to a ".1" suffix and starts a fresh file
// containing only the new entry. The pre-existing file is grown to the
// threshold via Truncate (a sparse file) rather than actually writing 10MB,
// since only its reported Size() matters to rotateIfNeeded.
func TestAttributionLoggerRotatesPastSizeThreshold(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "attribution.jsonl")
	if err := os.WriteFile(logPath, []byte("old-generation-marker\n"), 0o644); err != nil {
		t.Fatalf("seed old log: %v", err)
	}
	if err := os.Truncate(logPath, maxAttributionLogBytes); err != nil {
		t.Fatalf("grow old log to rotation threshold: %v", err)
	}

	logger, err := NewAttributionLogger(dir)
	if err != nil {
		t.Fatalf("NewAttributionLogger() error = %v", err)
	}
	if err := logger.Log(AttributionEntry{DelegateName: "after-rotation"}); err != nil {
		t.Fatalf("Log() error = %v", err)
	}

	rotatedInfo, err := os.Stat(logPath + ".1")
	if err != nil {
		t.Fatalf("stat rotated file %s.1: %v", logPath, err)
	}
	if rotatedInfo.Size() < maxAttributionLogBytes {
		t.Errorf("rotated file size = %d, want >= %d (the pre-rotation file)", rotatedInfo.Size(), maxAttributionLogBytes)
	}

	freshData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fresh log: %v", err)
	}
	var fresh struct {
		DelegateName string `json:"delegate_name"`
	}
	if err := json.Unmarshal(freshData, &fresh); err != nil {
		t.Fatalf("fresh log line not valid JSON: %v (data: %q)", err, freshData)
	}
	if fresh.DelegateName != "after-rotation" {
		t.Errorf("fresh log delegate_name = %q, want after-rotation", fresh.DelegateName)
	}
}

// TestAttributionLoggerCreatesDataDirIfMissing covers NewAttributionLogger's
// own documented behavior of creating dataDir if it does not already exist.
func TestAttributionLoggerCreatesDataDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet-created", "nested")
	if _, err := NewAttributionLogger(dir); err != nil {
		t.Fatalf("NewAttributionLogger() error = %v, want it to create the directory", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("dataDir %s was not created", dir)
	}
}
