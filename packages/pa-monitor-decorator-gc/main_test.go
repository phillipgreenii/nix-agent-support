package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestRun_EmitsLabelsKey is the scaffold's only test: pipe a sample
// session JSON through run() and assert the output is well-formed
// JSON with a top-level "labels" object. Once the actual mapping
// lands, tests asserting on specific label values go beside this one.
func TestRun_EmitsLabelsKey(t *testing.T) {
	sample := `{
		"ID": "gc-5z36",
		"PID": 25155,
		"CWD": "/Users/phillipg/gc/.gc/agents/deacon",
		"Env": {
			"GC_AGENT": "pgii-gastown.deacon",
			"GC_CITY": "/Users/phillipg/gc",
			"GC_PROVIDER": "claude",
			"GC_TEMPLATE": "pgii-gastown.deacon",
			"GC_SESSION_ORIGIN": "named"
		},
		"Model": "claude-opus-4-1"
	}`

	var out bytes.Buffer
	if err := run(strings.NewReader(sample), &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out.String())
	}
	labels, ok := parsed["labels"]
	if !ok {
		t.Fatalf("output missing top-level \"labels\" key: %v", parsed)
	}
	if _, ok := labels.(map[string]any); !ok {
		t.Fatalf("\"labels\" is not an object: %T", labels)
	}
}

// TestRun_EmptyInput verifies the runner is resilient to an empty
// stdin — the daemon may probe us with zero bytes during startup.
func TestRun_EmptyInput(t *testing.T) {
	var out bytes.Buffer
	if err := run(strings.NewReader(""), &out); err != nil {
		t.Fatalf("run on empty input returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"labels"`) {
		t.Fatalf("empty-input output missing labels key: %q", out.String())
	}
}
