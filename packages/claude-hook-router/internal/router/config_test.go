package router

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEventMatchField covers the per-event matcher-target field mapping
// (ADR 0071 §2.3, ADR 0071 §4 Phase B, B2 "Event selection ... then
// per-delegate matcher filtering against the event-appropriate field").
func TestEventMatchField(t *testing.T) {
	cases := []struct {
		event     string
		wantField string
		wantOK    bool
	}{
		{"PreToolUse", "tool_name", true},
		{"PostToolUse", "tool_name", true},
		{"PostToolUseFailure", "tool_name", true},
		{"PermissionRequest", "tool_name", true},
		{"PermissionDenied", "tool_name", true},
		{"SessionEnd", "end_reason", true},
		{"SessionStart", "session_start_reason", true},
		{"SomeFutureEventThisRouterDoesNotKnowAbout", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			field, ok := EventMatchField(tc.event)
			if field != tc.wantField || ok != tc.wantOK {
				t.Errorf("EventMatchField(%q) = (%q, %v), want (%q, %v)", tc.event, field, ok, tc.wantField, tc.wantOK)
			}
		})
	}
}

// TestLoadConfig covers the happy path and the two failure shapes
// LoadConfig's caller (main.go's dispatch()) relies on to trigger the
// total-failure fallback: an unreadable file and unparseable JSON.
func TestLoadConfig(t *testing.T) {
	t.Run("happy path parses the event -> delegate-list shape", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "router-config.json")
		const body = `{
			"PreToolUse": [
				{"name": "a", "event": "PreToolUse", "matcher": "Bash", "command": "true", "contract": "decide", "priority": 1}
			]
		}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write fixture config: %v", err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v, want nil", err)
		}
		delegates, ok := cfg["PreToolUse"]
		if !ok || len(delegates) != 1 {
			t.Fatalf("cfg[%q] = %#v, want exactly one delegate", "PreToolUse", delegates)
		}
		want := Delegate{Name: "a", Event: "PreToolUse", Matcher: "Bash", Command: "true", Contract: "decide", Priority: 1}
		if delegates[0] != want {
			t.Errorf("delegates[0] = %#v, want %#v", delegates[0], want)
		}
	})

	t.Run("missing file is an error, never a panic", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := LoadConfig(filepath.Join(dir, "does-not-exist.json")); err == nil {
			t.Error("LoadConfig() on a missing file returned nil error, want a total-failure-triggering error")
		}
	})

	t.Run("malformed JSON is an error, never a panic", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "router-config.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write fixture config: %v", err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Error("LoadConfig() on malformed JSON returned nil error, want a total-failure-triggering error")
		}
	})
}
