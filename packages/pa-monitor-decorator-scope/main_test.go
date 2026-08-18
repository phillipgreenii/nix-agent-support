package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

// --- runWith error paths -------------------------------------------------

// errReader fails on the first Read, so tests can drive runWith's stdin
// read-failure path without a real file descriptor.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("simulated stdin failure") }

// TestRunWith_ReadErrorPropagates: a stdin read failure MUST surface as an
// error (the daemon then swallows our output) rather than being silently
// downgraded to an empty session with empty labels.
func TestRunWith_ReadErrorPropagates(t *testing.T) {
	var out bytes.Buffer
	err := runWith(errReader{}, &out, []rule{{prefix: "/a", scope: "a"}})
	if err == nil {
		t.Fatalf("runWith must return an error when stdin cannot be read; output was %q", out.String())
	}
	if !strings.Contains(err.Error(), "read stdin") {
		t.Fatalf("error should name the read stage, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("nothing may be written when the read fails, got %q", out.String())
	}
}

// TestRunWith_MalformedJSONPropagates: undecodable stdin MUST surface as an
// error rather than being treated as a zero-value session.
func TestRunWith_MalformedJSONPropagates(t *testing.T) {
	var out bytes.Buffer
	err := runWith(strings.NewReader(`{"CWD":`), &out, []rule{{prefix: "/a", scope: "a"}})
	if err == nil {
		t.Fatalf("runWith must return an error on malformed session JSON; output was %q", out.String())
	}
	if !strings.Contains(err.Error(), "parse session JSON") {
		t.Fatalf("error should name the parse stage, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("nothing may be written when the decode fails, got %q", out.String())
	}
}

// TestRunWith_NoMatchEmitsEmptyLabelsObject pins the WIRE contract the
// pa-monitor daemon parses: a session that matches no rule emits an empty
// labels OBJECT, never a null. A nil map would render as {"labels":null}.
func TestRunWith_NoMatchEmitsEmptyLabelsObject(t *testing.T) {
	sample := `{"ID":"s1","CWD":"/Users/phillipg/personal/repo"}`
	rules := []rule{{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"}}

	var out bytes.Buffer
	if err := runWith(strings.NewReader(sample), &out, rules); err != nil {
		t.Fatalf("runWith returned error: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != `{"labels":{}}` {
		t.Fatalf("no-match session must emit an empty labels object, got %q", got)
	}
}

// --- rule-selection edges ------------------------------------------------

// TestDecorate_EmptyPrefixRuleSkipped: a rule whose prefix normalises to the
// empty string is skipped REGARDLESS of its scope. Without that guard an empty
// prefix matches every absolute CWD (HasPrefix(cwd, "/")), blanketing every
// session with a scope nobody configured.
func TestDecorate_EmptyPrefixRuleSkipped(t *testing.T) {
	for _, prefix := range []string{"", "/", "//"} {
		rules := []rule{{prefix: prefix, scope: "bogus"}}
		if got := decorate(session{CWD: "/Users/phillipg/personal/repo"}, rules); len(got) != 0 {
			t.Fatalf("rule with prefix %q must be skipped, got %v", prefix, got)
		}
	}
}

// TestDecorate_EmptyScopeRuleSkipped: a rule with an empty scope is skipped
// REGARDLESS of its prefix. The empty-scope rule here has the longer prefix,
// so if it participated in matching it would win the longest-prefix contest
// and blank out the label the shorter valid rule produced.
func TestDecorate_EmptyScopeRuleSkipped(t *testing.T) {
	rules := []rule{
		{prefix: "/Volumes/ziprecruiter", scope: "ziprecruiter"},
		{prefix: "/Volumes/ziprecruiter/blank", scope: ""},
	}
	got := decorate(session{CWD: "/Volumes/ziprecruiter/blank/deep"}, rules)
	if got["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("empty-scope rule must be skipped, not win the prefix contest: got %v, want ziprecruiter", got)
	}
}

// TestDecorate_EqualLengthPrefixTieBreak pins the longest-prefix TIE-BREAK:
// when two rules normalise to prefixes of equal length that both match, the
// EARLIER rule wins (the comparison is strictly-greater, not
// greater-or-equal). Asserted in both input orders so it pins the ORDER rule
// and not one particular scope value.
func TestDecorate_EqualLengthPrefixTieBreak(t *testing.T) {
	// "/a/b" and "/a/b/" normalise to the same prefix via TrimRight.
	rules := []rule{
		{prefix: "/a/b", scope: "first"},
		{prefix: "/a/b/", scope: "second"},
	}
	if got := decorate(session{CWD: "/a/b/c"}, rules); got["workspace.scope"] != "first" {
		t.Fatalf("equal-length prefix tie must go to the earlier rule: got %v, want first", got)
	}
	rules[0], rules[1] = rules[1], rules[0]
	if got := decorate(session{CWD: "/a/b/c"}, rules); got["workspace.scope"] != "second" {
		t.Fatalf("equal-length prefix tie must go to the earlier rule: got %v, want second", got)
	}
}

// --- parseRuleEntry edges ------------------------------------------------

// TestParseRuleEntry_RejectsEntryWithoutPrefix: an entry whose '=' is at index
// 0 has no prefix at all ("=personal") and MUST be rejected — the guard is
// `i <= 0`, not `i < 0`.
func TestParseRuleEntry_RejectsEntryWithoutPrefix(t *testing.T) {
	for _, entry := range []string{"=personal", "=", " =personal"} {
		if prefix, scope, ok := parseRuleEntry(entry); ok {
			t.Fatalf("parseRuleEntry(%q) = (%q, %q, true), want rejected", entry, prefix, scope)
		}
	}
}

// TestParseRuleEntry_RejectsWhitespaceOnlySides: the rejection happens AFTER
// trimming, so a side that is only whitespace counts as empty.
func TestParseRuleEntry_RejectsWhitespaceOnlySides(t *testing.T) {
	for _, entry := range []string{"   =personal", "/x=   ", "\t=\t", "/x="} {
		if prefix, scope, ok := parseRuleEntry(entry); ok {
			t.Fatalf("parseRuleEntry(%q) = (%q, %q, true), want rejected", entry, prefix, scope)
		}
	}
}

// TestParseRuleEntry_TrimsBothSides: the positive counterpart — surrounding
// whitespace is trimmed off an otherwise valid entry rather than becoming part
// of the prefix or scope.
func TestParseRuleEntry_TrimsBothSides(t *testing.T) {
	prefix, scope, ok := parseRuleEntry("  /Volumes/zr  =  ziprecruiter  ")
	if !ok {
		t.Fatalf("padded but valid entry was rejected")
	}
	if prefix != "/Volumes/zr" || scope != "ziprecruiter" {
		t.Fatalf("got (%q, %q), want (\"/Volumes/zr\", \"ziprecruiter\")", prefix, scope)
	}
}
