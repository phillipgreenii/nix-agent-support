package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeActivityConfig writes a registry whose top-level activity.sources
// lists backends (in order) and, when blocks[b] is non-empty, a
// backends.<b> block, then points $PG_PR_CONFIG at it.
func writeActivityListConfig(t *testing.T, backends []string, blocks map[string]string) {
	t.Helper()
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("activity:\n  sources:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	if len(blocks) > 0 {
		sb.WriteString("backends:\n")
		for b, block := range blocks {
			sb.WriteString("  " + b + ": " + block + "\n")
		}
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
}

// activityItemJSON is one wire-valid ActivityItem with the given id.
func activityItemJSON(id string) string {
	return `{"id":"` + id + `","kind":"pr.merged","entity_type":"pr","entity_id":"o/r#1","occurred_at":"2026-10-01T12:00:00Z","summary":"merged ` + id + `","fields":{},"as_of":"2026-10-02T00:00:00Z","stale":false}`
}

func activityResp(truncated string, ids ...string) string {
	items := make([]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, activityItemJSON(id))
	}
	return `{"protocolVersion":1,"schemaVersion":1,"result":{"items":[` + strings.Join(items, ",") + `],"truncated":` + truncated + `}}`
}

const (
	activityUnknownOpResp    = `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op list_activity"}}`
	activityUnavailableResp  = `{"protocolVersion":1,"error":{"code":"unavailable","message":"upstream down"}}`
	activityNullItemsResp    = `{"protocolVersion":1,"schemaVersion":1,"result":{"items":null,"truncated":false}}`
	activityEmptyHealthyResp = `{"protocolVersion":1,"schemaVersion":1,"result":{"items":[],"truncated":false}}`
)

// activityOut is the decoded "activity list" JSON document.
type activityOut struct {
	Sources []map[string]any `json:"sources"`
	Items   []struct {
		Source string         `json:"source"`
		Item   map[string]any `json:"item"`
	} `json:"items"`
}

func decodeActivityOut(t *testing.T, stdout string) activityOut {
	t.Helper()
	var o activityOut
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return o
}

// capturedActivityRequest decodes the op, args and config of the request a
// capturing backend recorded.
func capturedActivityRequest(t *testing.T, capturePath string) (op string, args, config map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("backend was never invoked: %v", err)
	}
	var req struct {
		Op     string         `json:"op"`
		Args   map[string]any `json:"args"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode captured request %q: %v", raw, err)
	}
	return req.Op, req.Args, req.Config
}

func TestActivityList_FanOutMixedSources_ConfigOrderAndRows(t *testing.T) {
	writeFakeBackend(t, "act-a", activityResp("true", "a1", "a2"))
	writeFakeBackend(t, "act-b", activityUnknownOpResp)
	writeFakeBackend(t, "act-c", activityUnavailableResp)
	writeFakeBackend(t, "act-d", activityResp("false", "d1"))
	writeActivityListConfig(t, []string{"act-a", "act-b", "act-c", "act-d"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (one degraded, others healthy); stdout=%s", code, stdout)
	}
	o := decodeActivityOut(t, stdout)
	if len(o.Sources) != 4 {
		t.Fatalf("sources = %v, want 4 rows", o.Sources)
	}
	want := []struct {
		source, status string
		count          float64
		truncated      bool
		reason         string
	}{
		{"act-a", "succeeded", 2, true, ""},
		{"act-b", "disabled", 0, false, "not applicable"},
		{"act-c", "degraded", 0, false, ""},
		{"act-d", "succeeded", 1, false, ""},
	}
	for i, w := range want {
		row := o.Sources[i]
		if row["source"] != w.source || row["status"] != w.status || row["count"] != w.count || row["truncated"] != w.truncated {
			t.Fatalf("sources[%d] = %v, want %+v", i, row, w)
		}
		reason, _ := row["reason"].(string)
		if w.reason != "" && reason != w.reason {
			t.Fatalf("sources[%d] reason = %q, want %q", i, reason, w.reason)
		}
		if w.status == "degraded" && reason == "" {
			t.Fatalf("degraded row has no reason: %v", row)
		}
	}
	// The degraded source leaves the others' items intact, concatenated in
	// config order with no merging or re-sorting.
	var got []string
	for _, it := range o.Items {
		got = append(got, it.Source+"/"+it.Item["id"].(string))
	}
	if strings.Join(got, ",") != "act-a/a1,act-a/a2,act-d/d1" {
		t.Fatalf("items = %v", got)
	}
	if o.Items[0].Item["summary"] != "merged a1" || o.Items[0].Item["kind"] != "pr.merged" {
		t.Fatalf("item was not passed through unmodified: %v", o.Items[0].Item)
	}
}

func TestActivityList_TruncatedIsWarningNotExitCode(t *testing.T) {
	writeFakeBackend(t, "act-trunc", activityResp("true", "t1"))
	writeFakeBackend(t, "act-na", activityUnknownOpResp)
	writeActivityListConfig(t, []string{"act-trunc", "act-na"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (truncated is a warning, disabled counts healthy); stdout=%s", code, stdout)
	}
	o := decodeActivityOut(t, stdout)
	if o.Sources[0]["truncated"] != true {
		t.Fatalf("truncated missing on row: %v", o.Sources[0])
	}
	if !strings.Contains(stdout, `"truncated":false`) {
		t.Fatalf("a non-truncated row must still carry truncated:false: %s", stdout)
	}
}

func TestActivityList_AllDegraded_ExitsThree(t *testing.T) {
	writeFakeBackend(t, "act-bad1", activityUnavailableResp)
	writeFakeBackend(t, "act-bad2", activityUnavailableResp)
	writeActivityListConfig(t, []string{"act-bad1", "act-bad2"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stdout=%s", code, stdout)
	}
}

func TestActivityList_NullItemsNormalized_AndEmptyMarshalsAsArray(t *testing.T) {
	writeFakeBackend(t, "act-nil", activityNullItemsResp)
	writeActivityListConfig(t, []string{"act-nil"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"items":[]`) || strings.Contains(stdout, "null") {
		t.Fatalf("items must marshal as [] and never null: %s", stdout)
	}
	if o := decodeActivityOut(t, stdout); len(o.Sources) != 1 || o.Sources[0]["count"] != float64(0) {
		t.Fatalf("sources = %v", o.Sources)
	}
}

func TestActivityList_NoActivitySources_EmptyArraysAndExitThree(t *testing.T) {
	// A registry with no activity.sources key at all.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - act-unused\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (zero sources queried); stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"sources":[]`) || !strings.Contains(stdout, `"items":[]`) {
		t.Fatalf("empty registry must yield sources:[] and items:[]: %s", stdout)
	}
}

func TestActivityList_BackendPinInvokesOnlyThatBackend(t *testing.T) {
	dir := t.TempDir()
	capA := filepath.Join(dir, "a.json")
	capB := filepath.Join(dir, "b.json")
	writeCapturingFakeBackend(t, "act-pin-a", capA, activityResp("false", "a1"))
	writeCapturingFakeBackend(t, "act-pin-b", capB, activityResp("false", "b1"))
	writeActivityListConfig(t, []string{"act-pin-a", "act-pin-b"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h", "--backend", "act-pin-b"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	if _, err := os.Stat(capA); err == nil {
		t.Fatal("unpinned backend act-pin-a was invoked")
	}
	if _, err := os.Stat(capB); err != nil {
		t.Fatalf("pinned backend act-pin-b was not invoked: %v", err)
	}
	o := decodeActivityOut(t, stdout)
	if len(o.Sources) != 1 || o.Sources[0]["source"] != "act-pin-b" || len(o.Items) != 1 || o.Items[0].Source != "act-pin-b" {
		t.Fatalf("pinned output = %s", stdout)
	}
}

func TestActivityList_BackendPinUnregisteredFails(t *testing.T) {
	capPath := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "act-reg", capPath, activityEmptyHealthyResp)
	writeActivityListConfig(t, []string{"act-reg"}, nil)

	_, _, code := executePr(t, []string{"activity", "list", "--since", "24h", "--backend", "act-other"})
	if code == 0 {
		t.Fatal("an unregistered --backend pin must fail")
	}
	if _, err := os.Stat(capPath); err == nil {
		t.Fatal("a backend was invoked despite the bad pin")
	}
}

func TestActivityList_RequestCarriesRangeInArgsNeverConfig(t *testing.T) {
	capPath := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "act-req", capPath, activityEmptyHealthyResp)
	writeActivityListConfig(t, []string{"act-req"}, map[string]string{"act-req": `{"static_key":"keep"}`})

	started := time.Now()
	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "7d"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	op, args, config := capturedActivityRequest(t, capPath)
	if op != "list_activity" {
		t.Fatalf("op = %q, want list_activity", op)
	}
	since, err := time.Parse(time.RFC3339, args["since"].(string))
	if err != nil {
		t.Fatalf("since %v is not RFC3339: %v", args["since"], err)
	}
	if want := started.Add(-7 * 24 * time.Hour); since.Before(want.Add(-time.Minute)) || since.After(want.Add(time.Minute)) {
		t.Fatalf("since = %s, want about %s", since, want)
	}
	before, err := time.Parse(time.RFC3339, args["before"].(string))
	if err != nil {
		t.Fatalf("before %v is not RFC3339: %v", args["before"], err)
	}
	if before.Before(started.Add(-time.Minute)) || before.After(time.Now().Add(time.Minute)) {
		t.Fatalf("before = %s, want about now", before)
	}
	if config["static_key"] != "keep" {
		t.Fatalf("static backends block was dropped: %v", config)
	}
	for k := range config {
		if strings.Contains(k, "since") || strings.Contains(k, "before") || strings.HasPrefix(k, "list_") || strings.HasPrefix(k, "search_") {
			t.Fatalf("config carries range key %q: %v", k, config)
		}
	}
}

func TestActivityList_OmittedSinceSendsNoSinceAndBeforeDefaultsToNow(t *testing.T) {
	capPath := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "act-open", capPath, activityEmptyHealthyResp)
	writeActivityListConfig(t, []string{"act-open"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	_, args, _ := capturedActivityRequest(t, capPath)
	if _, present := args["since"]; present {
		t.Fatalf("open-ended range must send no since: %v", args)
	}
	if _, ok := args["before"].(string); !ok {
		t.Fatalf("before must always be sent: %v", args)
	}
}

func TestActivityList_ExplicitRFC3339BoundsPassedThrough(t *testing.T) {
	capPath := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "act-abs", capPath, activityEmptyHealthyResp)
	writeActivityListConfig(t, []string{"act-abs"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "2026-09-01T00:00:00Z", "--before", "2026-09-08T00:00:00Z"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	_, args, _ := capturedActivityRequest(t, capPath)
	if args["since"] != "2026-09-01T00:00:00Z" || args["before"] != "2026-09-08T00:00:00Z" {
		t.Fatalf("args = %v", args)
	}
}

func TestActivityList_BoundSyntaxesAccepted(t *testing.T) {
	for _, since := range []string{"7d", "168h", "2026-09-01T00:00:00Z"} {
		t.Run(since, func(t *testing.T) {
			writeFakeBackend(t, "act-syntax", activityEmptyHealthyResp)
			writeActivityListConfig(t, []string{"act-syntax"}, nil)
			stdout, _, code := executePr(t, []string{"activity", "list", "--since", since})
			if code != 0 {
				t.Fatalf("--since %s: exit = %d; stdout=%s", since, code, stdout)
			}
		})
	}
}

func TestActivityList_InvalidBoundsFailWithoutInvokingBackend(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	tests := []struct {
		name string
		args []string
	}{
		{"garbage since", []string{"--since", "nope"}},
		{"garbage before", []string{"--since", "24h", "--before", "soon"}},
		{"since later than before", []string{"--since", "1d", "--before", "7d"}},
		{"since equals before", []string{"--since", "2026-10-01T00:00:00Z", "--before", "2026-10-01T00:00:00Z"}},
		{"since later than defaulted now", []string{"--since", future}},
		{"negative duration", []string{"--since", "-5h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capPath := filepath.Join(t.TempDir(), "req.json")
			writeCapturingFakeBackend(t, "act-invalid", capPath, activityEmptyHealthyResp)
			writeActivityListConfig(t, []string{"act-invalid"}, nil)

			stdout, _, code := executePr(t, append([]string{"activity", "list"}, tt.args...))
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero; stdout=%s", stdout)
			}
			if !strings.Contains(stdout, "invalid_argument") {
				t.Fatalf("stdout does not report invalid_argument: %s", stdout)
			}
			if _, err := os.Stat(capPath); err == nil {
				t.Fatal("a backend was invoked despite the invalid bound")
			}
		})
	}
}

func TestActivityList_HelpDocumentsBoundSyntax(t *testing.T) {
	stdout, _, code := executePr(t, []string{"activity", "list", "--help"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	for _, want := range []string{"RFC3339", "168h", "7d", "--since", "--before", "--backend", "now"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestActivityList_HumanOutput(t *testing.T) {
	writeFakeBackend(t, "act-human", activityResp("true", "h1"))
	writeActivityListConfig(t, []string{"act-human"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h", "--output", "human"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	for _, want := range []string{"activity (1):", "merged h1", "act-human", "truncated"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q:\n%s", want, stdout)
		}
	}
}

func TestActivityOutcome_ExitCodeMatchesFanOutScheme(t *testing.T) {
	row := func(s SourceStatus, truncated bool) activitySourceRow {
		return activitySourceRow{Source: "x", Status: s, Truncated: truncated}
	}
	tests := []struct {
		name string
		rows []activitySourceRow
		want int
	}{
		{"no sources", nil, 3},
		{"all succeeded", []activitySourceRow{row(SourceSucceeded, false)}, 0},
		{"truncated never changes it", []activitySourceRow{row(SourceSucceeded, true)}, 0},
		{"disabled counts healthy", []activitySourceRow{row(SourceDisabled, false)}, 0},
		{"degraded among healthy", []activitySourceRow{row(SourceSucceeded, false), row(SourceDegraded, false)}, 2},
		{"all degraded", []activitySourceRow{row(SourceDegraded, false)}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (ActivityOutcome{Sources: tt.rows}).ExitCode(); got != tt.want {
				t.Fatalf("ExitCode = %d, want %d", got, tt.want)
			}
		})
	}
}
