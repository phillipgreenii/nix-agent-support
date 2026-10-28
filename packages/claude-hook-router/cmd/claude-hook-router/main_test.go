package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Total-failure fallback -------------------------------------------------
//
// ADR 0071 §4 Phase B, B1 "Total-failure fallback: router-config.json
// unreadable (or similar) ⇒ router returns {}, never hangs or silently
// denies." These tests exercise dispatch() (the function run() wraps)
// directly, since that is where every total-failure branch lives.

func TestDispatchFailsSafeWhenPluginRootIsUnset(t *testing.T) {
	out := dispatch(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), &bytes.Buffer{}, map[string]string{})
	if !out.IsZero() {
		t.Errorf("output = %#v, want the zero HookOutput", out)
	}
}

func TestDispatchFailsSafeWhenRouterConfigIsMissing(t *testing.T) {
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": t.TempDir()}
	out := dispatch(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), &bytes.Buffer{}, env)
	if !out.IsZero() {
		t.Errorf("output = %#v, want the zero HookOutput", out)
	}
}

func TestDispatchFailsSafeWhenRouterConfigIsMalformed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "router-config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": root}
	out := dispatch(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), &bytes.Buffer{}, env)
	if !out.IsZero() {
		t.Errorf("output = %#v, want the zero HookOutput", out)
	}
}

func TestDispatchFailsSafeWhenStdinJSONIsMalformed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "router-config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": root}
	out := dispatch(strings.NewReader("not json"), &bytes.Buffer{}, env)
	if !out.IsZero() {
		t.Errorf("output = %#v, want the zero HookOutput", out)
	}
}

func TestDispatchAbstainsWhenNoDelegatesRegisteredForEvent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "router-config.json"), []byte(`{"PostToolUse":[]}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": root}
	out := dispatch(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), &bytes.Buffer{}, env)
	if !out.IsZero() {
		t.Errorf("output = %#v, want the zero HookOutput (no PreToolUse registration at all)", out)
	}
}

// TestRunAlwaysExitsZeroAndPrintsValidJSONEvenOnTotalFailure covers the
// run() wrapper's own contract: whatever dispatch() decides, run() prints
// exactly one line of valid JSON to stdout and always returns exit code 0
// (ADR 0071 §5.4 "Fail-safe default on total router failure").
func TestRunAlwaysExitsZeroAndPrintsValidJSONEvenOnTotalFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader("not json"), &stdout, &stderr, []string{})

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout: %q)", err, stdout.String())
	}
	if len(out) != 0 {
		t.Errorf("stdout JSON = %v, want {} (fail-safe abstain)", out)
	}
}

// --- Event selection + matcher filtering ------------------------------------

// TestDispatchEventSelectionAndMatcherFiltering covers "Event selection
// from hook_event_name, then per-delegate matcher filtering against the
// event-appropriate field, before dispatch" (ADR 0071 §4 Phase B, B1/B2):
// an unmatched-event delegate never runs, and among same-event delegates
// only the ones whose matcher matches tool_name actually run.
func TestDispatchEventSelectionAndMatcherFiltering(t *testing.T) {
	root := t.TempDir()
	config := `{
		"PreToolUse": [
			{"name": "bash-only", "event": "PreToolUse", "matcher": "Bash", "command": "printf %s '{\"additionalContext\":\"bash-only\"}'", "contract": "annotate", "priority": 1},
			{"name": "write-or-edit", "event": "PreToolUse", "matcher": "Write|Edit", "command": "printf %s '{\"additionalContext\":\"write-or-edit\"}'", "contract": "annotate", "priority": 2},
			{"name": "match-all", "event": "PreToolUse", "matcher": "*", "command": "printf %s '{\"additionalContext\":\"match-all\"}'", "contract": "annotate", "priority": 3}
		],
		"PostToolUse": [
			{"name": "post-marker", "event": "PostToolUse", "matcher": "*", "command": "printf %s '{\"additionalContext\":\"should-not-run-for-pretooluse\"}'", "contract": "annotate", "priority": 1}
		]
	}`
	if err := os.WriteFile(filepath.Join(root, "router-config.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": root, "CLAUDE_PLUGIN_DATA": t.TempDir()}

	out := dispatch(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`), &bytes.Buffer{}, env)

	want := "bash-only\nmatch-all"
	if out.AdditionalContext != want {
		t.Errorf("AdditionalContext = %q, want %q (only the matching-tool_name and match-all delegates for THIS event)", out.AdditionalContext, want)
	}
}

// --- Attribution log (integration, through dispatch()) ----------------------

// TestAttributionLogWrittenForRepresentativeMultiDelegateCall covers
// "Attribution log: assert the JSON-lines log is written correctly (per
// B1's concrete format) for a representative multi-delegate call" at the
// main.go wiring level (dispatch() is what actually constructs the
// AttributionLogger and calls Log() per entry).
func TestAttributionLogWrittenForRepresentativeMultiDelegateCall(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	config := `{
		"PreToolUse": [
			{"name": "decider", "event": "PreToolUse", "matcher": "*", "command": "printf %s '{\"permissionDecision\":\"ask\"}'", "contract": "decide", "priority": 1},
			{"name": "abstainer", "event": "PreToolUse", "matcher": "*", "command": "printf %s '{}'", "contract": "decide", "priority": 2}
		]
	}`
	if err := os.WriteFile(filepath.Join(root, "router-config.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": root, "CLAUDE_PLUGIN_DATA": dataDir}

	dispatch(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`), &bytes.Buffer{}, env)

	data, err := os.ReadFile(filepath.Join(dataDir, "attribution.jsonl"))
	if err != nil {
		t.Fatalf("read attribution.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d attribution log lines, want 2: %q", len(lines), data)
	}

	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 1 not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("line 2 not valid JSON: %v", err)
	}

	for _, key := range []string{"timestamp", "hook_event_name", "delegate_name", "contract", "verdict", "duration_ms"} {
		if _, ok := first[key]; !ok {
			t.Errorf("line 1 missing key %q: %v", key, first)
		}
	}
	if first["delegate_name"] != "decider" || first["verdict"] != "applied" {
		t.Errorf("line 1 = %v, want delegate_name=decider verdict=applied", first)
	}
	if second["delegate_name"] != "abstainer" || second["verdict"] != "abstain" {
		t.Errorf("line 2 = %v, want delegate_name=abstainer verdict=abstain", second)
	}
}
