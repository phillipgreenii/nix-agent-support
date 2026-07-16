package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestRun_EmitsLabelsKey mirrors the standard decorator scaffold test: pipe a
// sample session JSON through run() and assert the output is well-formed
// JSON with a top-level "labels" object. Under `go test` no -rule flags and
// no env rules reach run(), so the labels are empty — that is still a valid
// envelope.
func TestRun_EmitsLabelsKey(t *testing.T) {
	t.Setenv(envRules, "") // deterministic: no ambient rules from the shell
	sample := `{
		"ID": "zr-5z36",
		"PID": 25155,
		"CWD": "/Users/phillipg/code/service",
		"Env": {"GC_PROVIDER": "claude"},
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

// TestRun_EmptyInput verifies the runner is resilient to empty stdin — the
// daemon may probe with zero bytes at startup.
func TestRun_EmptyInput(t *testing.T) {
	t.Setenv(envRules, "")
	var out bytes.Buffer
	if err := run(strings.NewReader(""), &out); err != nil {
		t.Fatalf("run on empty input returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"labels"`) {
		t.Fatalf("empty-input output missing labels key: %q", out.String())
	}
}

// TestRunWith_MapsCWDToScope drives the read->decorate->write path with an
// explicit rule set and asserts the mapped scope lands in the JSON envelope.
func TestRunWith_MapsCWDToScope(t *testing.T) {
	sample := `{"ID":"s1","CWD":"/Volumes/ziprecruiter/x"}`
	rules := []rule{{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"}}

	var out bytes.Buffer
	if err := runWith(strings.NewReader(sample), &out, rules); err != nil {
		t.Fatalf("runWith returned error: %v", err)
	}
	var parsed output
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out.String())
	}
	if parsed.Labels["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("workspace.scope = %q, want ziprecruiter; full=%v", parsed.Labels["workspace.scope"], parsed.Labels)
	}
}

// TestDecorate_MatchEmitsScope: the canonical mapping case.
func TestDecorate_MatchEmitsScope(t *testing.T) {
	rules := []rule{{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"}}
	got := decorate(session{CWD: "/Volumes/ziprecruiter/x"}, rules)
	if got["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("got %v, want workspace.scope=ziprecruiter", got)
	}
}

// TestDecorate_LongestPrefixWins: with two matching prefixes the longer one
// determines the scope.
func TestDecorate_LongestPrefixWins(t *testing.T) {
	rules := []rule{
		{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"},
		{prefix: "/Volumes/ziprecruiter/special", scope: "special"},
	}
	// Rule order should not matter: try both orderings.
	for _, order := range [][]rule{rules, {rules[1], rules[0]}} {
		got := decorate(session{CWD: "/Volumes/ziprecruiter/special/deep/dir"}, order)
		if got["workspace.scope"] != "special" {
			t.Fatalf("longest-prefix failed for order %v: got %v, want special", order, got)
		}
	}
	// A CWD under only the shorter prefix keeps the shorter scope.
	got := decorate(session{CWD: "/Volumes/ziprecruiter/other"}, rules)
	if got["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("shorter-prefix CWD: got %v, want ziprecruiter", got)
	}
}

// TestDecorate_NonMatchingCWD_EmptyLabels: no rule matches -> empty labels so
// the daemon's DefaultScope ("personal") stands.
func TestDecorate_NonMatchingCWD_EmptyLabels(t *testing.T) {
	rules := []rule{{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"}}
	got := decorate(session{CWD: "/Users/phillipg/personal"}, rules)
	if len(got) != 0 {
		t.Fatalf("non-matching CWD should yield empty labels, got %v", got)
	}
}

// TestDecorate_SegmentBoundary: a sibling path that merely shares a string
// prefix (but not a path-segment boundary) MUST NOT match.
func TestDecorate_SegmentBoundary(t *testing.T) {
	rules := []rule{{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"}}
	got := decorate(session{CWD: "/Volumes/ziprecruiterX/y"}, rules)
	if len(got) != 0 {
		t.Fatalf("string-prefix-but-not-segment CWD should not match, got %v", got)
	}
	// An exact-prefix CWD (equal to the prefix) still matches.
	if got := decorate(session{CWD: "/Volumes/ziprecruiter"}, rules); got["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("exact-prefix CWD should match, got %v", got)
	}
}

// TestDecorate_NoRules_EmptyLabels: with no rules configured, always empty.
func TestDecorate_NoRules_EmptyLabels(t *testing.T) {
	if got := decorate(session{CWD: "/Volumes/ziprecruiter/x"}, nil); len(got) != 0 {
		t.Fatalf("no rules should yield empty labels, got %v", got)
	}
}

// TestDecorate_EmptySession_EmptyLabels: an empty (zero-value) session has no
// CWD and matches nothing.
func TestDecorate_EmptySession_EmptyLabels(t *testing.T) {
	rules := []rule{{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"}}
	if got := decorate(session{}, rules); len(got) != 0 {
		t.Fatalf("empty session should yield empty labels, got %v", got)
	}
}

// TestLoadRules_EnvVarParsing: rules come from PA_MONITOR_SCOPE_RULES as
// `PREFIX=SCOPE` entries separated by ';'.
func TestLoadRules_EnvVarParsing(t *testing.T) {
	rules := loadRules(nil, "/Volumes/ziprecruiter=ziprecruiter; /Users/phillipg/work=work")
	got := decorate(session{CWD: "/Volumes/ziprecruiter/x"}, rules)
	if got["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("env rule 1 not applied: %v", got)
	}
	got = decorate(session{CWD: "/Users/phillipg/work/repo"}, rules)
	if got["workspace.scope"] != "work" {
		t.Fatalf("env rule 2 not applied: %v", got)
	}
}

// TestLoadRules_FlagParsing: repeatable -rule flags are parsed.
func TestLoadRules_FlagParsing(t *testing.T) {
	rules := loadRules([]string{"-rule", "/Volumes/ziprecruiter=ziprecruiter", "-rule", "/a=b"}, "")
	if got := decorate(session{CWD: "/Volumes/ziprecruiter/x"}, rules); got["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("flag rule 1 not applied: %v", got)
	}
	if got := decorate(session{CWD: "/a/deep"}, rules); got["workspace.scope"] != "b" {
		t.Fatalf("flag rule 2 not applied: %v", got)
	}
}

// TestLoadRules_FlagOverridesEnv: a flag rule for the same prefix wins over
// the env fallback.
func TestLoadRules_FlagOverridesEnv(t *testing.T) {
	rules := loadRules([]string{"-rule", "/x=flag"}, "/x=env")
	if got := decorate(session{CWD: "/x/y"}, rules); got["workspace.scope"] != "flag" {
		t.Fatalf("flag should override env for same prefix: %v", got)
	}
}

// TestLoadRules_IgnoresMalformed: entries without a '=' or with an empty side
// are dropped rather than producing a bogus rule.
func TestLoadRules_IgnoresMalformed(t *testing.T) {
	rules := loadRules(nil, "no-equals; =noPrefix; /ok=good; /empty=")
	if len(rules) != 1 {
		t.Fatalf("expected 1 valid rule, got %d: %v", len(rules), rules)
	}
	if got := decorate(session{CWD: "/ok/here"}, rules); got["workspace.scope"] != "good" {
		t.Fatalf("valid rule not applied: %v", got)
	}
}
