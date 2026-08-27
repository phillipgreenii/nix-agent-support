package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// This file pins the `pg-pr open --json` machine-readable contract
// (pg2-4dz88.7.7). Per the bead's own JSON-shape decision, the top-level
// payload is a BARE JSON ARRAY of row objects — never an enveloped object
// like {"rows": [...]} — which is what makes "an empty selection is an empty
// array" well-defined. Every row repeats the snapshot's freshness scalars and
// carries a truncated flag for --max, because a machine consumer cannot read
// the stderr warnings a human does for either signal.

// jsonRows decodes stdout as the bare-array --json contract, failing the test
// if it is anything else (in particular, an enveloped object would fail
// here).
func jsonRows(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a bare JSON array of objects: %v\nstdout: %s", err, stdout)
	}
	return rows
}

// TestOpenJSONMode_Schema pins the per-row key set --json emits, and that the
// payload is a bare array (jsonRows already fails otherwise) matching every
// existing filter flag composed together.
func TestOpenJSONMode_Schema(t *testing.T) {
	opened, stdout, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true, jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if opened != nil {
		t.Fatalf("--json must never open a browser, got %v", opened)
	}

	rows := jsonRows(t, stdout)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (--all over the 3-row fixture)", len(rows))
	}

	wantKeys := []string{
		"number", "owner", "title", "url", "ci_status",
		"human_approvers", "agent_approvers", "files_changed", "lines_changed",
		"needs_attention", "match_reason", "hidden", "hidden_reason",
		"generated_at", "age_seconds", "stale", "stale_after_seconds", "truncated",
	}
	for _, key := range wantKeys {
		if _, present := rows[0][key]; !present {
			t.Errorf("row[0] missing key %q: %+v", key, rows[0])
		}
	}
	if got := rows[0]["number"]; got != float64(1) {
		t.Errorf(`rows[0]["number"] = %v, want 1`, got)
	}
	if got := rows[0]["owner"]; got != "alice" {
		t.Errorf(`rows[0]["owner"] = %v, want "alice"`, got)
	}
}

// TestOpenJSONMode_StaleFlagPresentOnEveryRead proves a stale snapshot yields
// stale:true (and the matching age/bound) in the JSON itself, on every row —
// not only the existing stderr warning.
func TestOpenJSONMode_StaleFlagPresentOnEveryRead(t *testing.T) {
	snap := openTestSnapshot()
	snap.Stale = true
	snap.AgeSeconds = 900
	snap.StaleAfterSeconds = 120

	_, stdout, _, err := runOpenCmd(t, snap, openFlags{all: true, jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}

	rows := jsonRows(t, stdout)
	if len(rows) == 0 {
		t.Fatal("want at least one row")
	}
	for i, r := range rows {
		if stale, _ := r["stale"].(bool); !stale {
			t.Errorf("row[%d][\"stale\"] = %v, want true", i, r["stale"])
		}
		if age, _ := r["age_seconds"].(float64); int(age) != 900 {
			t.Errorf("row[%d][\"age_seconds\"] = %v, want 900", i, r["age_seconds"])
		}
		if bound, _ := r["stale_after_seconds"].(float64); int(bound) != 120 {
			t.Errorf("row[%d][\"stale_after_seconds\"] = %v, want 120", i, r["stale_after_seconds"])
		}
	}
}

// TestOpenJSONMode_NoBrowserSideEffect proves --json never opens a browser,
// through the injected browser.OpenWindow seam (runOpenCmd's `opened` return
// is nil unless that seam was actually invoked) rather than by any observed
// side effect.
func TestOpenJSONMode_NoBrowserSideEffect(t *testing.T) {
	opened, _, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true, jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if opened != nil {
		t.Errorf("--json must never invoke the browser seam, got %v", opened)
	}
}

// TestOpenJSONMode_EmptySelectionIsEmptyArray pins the empty-selection
// acceptance criterion: --json emits a bare empty array and exits 0, never
// the human "(no PRs match)" message and never the browser seam.
func TestOpenJSONMode_EmptySelectionIsEmptyArray(t *testing.T) {
	snap := snapshot.Snapshot{Team: []snapshot.TeamRow{{Number: 1, URL: "u1", NeedsAttention: false}}}
	opened, stdout, _, err := runOpenCmd(t, snap, openFlags{jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil", err)
	}
	if opened != nil {
		t.Errorf("browser must not be launched for an empty --json selection, got %v", opened)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want a bare empty JSON array", stdout)
	}
	rows := jsonRows(t, stdout)
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
}

// TestOpenJSONMode_RowOrderMatchesHumanRenderer proves --json emits rows in
// exactly the order selectRows produced them — the same order the human
// renderer and the browser-tab order already use — over the same input.
func TestOpenJSONMode_RowOrderMatchesHumanRenderer(t *testing.T) {
	snap := snapshot.Snapshot{Team: []snapshot.TeamRow{
		{Number: 9, URL: "https://example.test/pull/9", NeedsAttention: true},
		{Number: 1, URL: "https://example.test/pull/1", NeedsAttention: true},
		{Number: 5, URL: "https://example.test/pull/5", NeedsAttention: true},
	}}

	openedURLs, _, _, err := runOpenCmd(t, snap, openFlags{})
	if err != nil {
		t.Fatalf("RunE() (human/browser path) error = %v", err)
	}

	_, stdout, _, err := runOpenCmd(t, snap, openFlags{jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() (--json path) error = %v", err)
	}
	rows := jsonRows(t, stdout)
	if len(rows) != len(openedURLs) {
		t.Fatalf("got %d JSON rows, want %d (matching the browser order)", len(rows), len(openedURLs))
	}
	for i, r := range rows {
		if got := r["url"]; got != openedURLs[i] {
			t.Errorf("row[%d][\"url\"] = %v, want %v (human/browser order)", i, got, openedURLs[i])
		}
	}
}

// TestOpenFlagValidation_JSONCombinations pins validateOpenFlags' explicit
// decision (pg2-4dz88.7.7) that --json and --print are contradictory, and
// that --json composes freely with every other flag.
func TestOpenFlagValidation_JSONCombinations(t *testing.T) {
	err := validateOpenFlags(openFlags{jsonOutput: true, printOnly: true})
	if err == nil {
		t.Fatal("validateOpenFlags(--json, --print) = nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "--json") || !strings.Contains(err.Error(), "--print") {
		t.Errorf("error %q does not name both flags", err)
	}

	for _, f := range []openFlags{
		{jsonOutput: true},
		{jsonOutput: true, all: true},
		{jsonOutput: true, mine: true},
		{jsonOutput: true, unapproved: true, max: 5},
		{printOnly: true},
	} {
		if err := validateOpenFlags(f); err != nil {
			t.Errorf("validateOpenFlags(%+v) = %v, want nil", f, err)
		}
	}
}

// TestOpenJSONMode_PGPROutputEnvSelectsJSON mirrors internal/prview's
// TestMarshalView_PGPROutputEnvSelectsJSONWithoutFlag for this seam:
// PGPR_OUTPUT=json selects JSON mode with no --json flag passed at all, and
// still never opens a browser.
func TestOpenJSONMode_PGPROutputEnvSelectsJSON(t *testing.T) {
	t.Setenv(output.EnvVar, "json")
	if !output.Resolve(false) {
		t.Fatalf("output.Resolve(false) = false, want true when %s=json", output.EnvVar)
	}

	opened, stdout, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if opened != nil {
		t.Errorf("%s=json must not open a browser, got %v", output.EnvVar, opened)
	}
	rows := jsonRows(t, stdout)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}

// TestOpenJSONMode_MaxTruncationIsVisible pins the decision that --max
// truncation is a JSON-visible fact, not only the existing stderr warning
// (TestOpenCmdMaxTruncatesAndWarns): every row in a truncated response
// carries truncated:true, and an untruncated response carries
// truncated:false throughout.
func TestOpenJSONMode_MaxTruncationIsVisible(t *testing.T) {
	_, stdout, stderr, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true, max: 2, jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	rows := jsonRows(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (--max)", len(rows))
	}
	for i, r := range rows {
		if truncated, _ := r["truncated"].(bool); !truncated {
			t.Errorf("row[%d][\"truncated\"] = %v, want true", i, r["truncated"])
		}
	}
	// The stderr warning must still fire alongside the JSON fact — --max's
	// human signal is not being replaced, only supplemented.
	if !strings.Contains(stderr, "--max") {
		t.Errorf("stderr does not still report the truncation: %q", stderr)
	}

	_, stdout2, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true, jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	for i, r := range jsonRows(t, stdout2) {
		if truncated, _ := r["truncated"].(bool); truncated {
			t.Errorf("row[%d][\"truncated\"] = %v, want false (no --max applied)", i, r["truncated"])
		}
	}
}

// ---------------------------------------------------------------------------
// Golden-fixture schema-stability test (pattern:
// internal/prview/prview_json_test.go). This bead deliberately ships before
// .7.2-.7.5's new snapshot/openRow fields land on the same structs, so a
// committed golden fixture is what makes a later field addition fail loudly
// here instead of silently changing the JSON contract this bead just shipped.
// ---------------------------------------------------------------------------

// openJSONFixtureRows builds the fixture openJSONRows the golden file
// describes, through openJSONRows itself (never a hand-built []openJSONRow
// literal) so it stays consistent with that function's own field mapping.
func openJSONFixtureRows() []openJSONRow {
	rows := []openRow{
		{
			Number: 101, Owner: "alice", Title: "Add contributor guide",
			URL: "https://example.test/pull/101", CIStatus: "success",
			HumanApprovers: 1, AgentApprovers: 0,
			FilesChanged: 12, LinesChanged: 340,
			NeedsAttention: true, MatchReason: []string{"review-requested"},
		},
		{
			Number: 202, Title: "Fix flaky test",
			URL: "https://example.test/pull/202", CIStatus: "failure",
			AgentApprovers: 2,
			Hidden:         true, HiddenReason: "duplicate work",
		},
	}
	snap := &snapshot.Snapshot{
		GeneratedAt:       time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
		AgeSeconds:        45,
		Stale:             false,
		StaleAfterSeconds: 120,
	}
	return openJSONRows(rows, snap, false)
}

func mustMarshalOpenJSONRows(t *testing.T, rows []openJSONRow) []byte {
	t.Helper()
	got, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	return got
}

func readOpenJSONGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return raw
}

// assertOpenJSONEqual compares two JSON documents structurally (decoded,
// then reflect.DeepEqual), not byte-for-byte — mirroring
// internal/prview/prview_json_test.go's assertJSONEqual and for the identical
// reason: a byte compare would couple this test to encoding/json's exact
// whitespace/array-wrapping style, which the repo's prettier pre-commit hook
// is free to reformat on every commit.
func assertOpenJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotDoc, wantDoc any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("unmarshal got: %v (input: %s)", err, got)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatalf("unmarshal want (golden fixture): %v (input: %s)", err, want)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Fatalf("openJSONRows output does not match golden fixture.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestOpenJSONRows_MatchesGolden pins `pg-pr open --json`'s row schema
// against a checked-in golden fixture (testdata/open-json-rows.json) so a
// later field addition — e.g. one of .7.2-.7.5's new snapshot fields —
// fails this test loudly instead of quietly changing the committed JSON
// contract.
func TestOpenJSONRows_MatchesGolden(t *testing.T) {
	got := mustMarshalOpenJSONRows(t, openJSONFixtureRows())
	want := readOpenJSONGolden(t, "open-json-rows.json")
	assertOpenJSONEqual(t, got, want)
}

// TestOpenJSONRows_JSONIsValid mirrors
// internal/prview/prview_json_test.go's TestMarshalView_Full_JSONIsValid /
// cmd/pg-pr/pr_test.go's TestPRInfo_JSONIsValid: json.Unmarshal of the
// ENTIRE marshaled output must succeed, pinning that nothing is ever
// appended after the array.
func TestOpenJSONRows_JSONIsValid(t *testing.T) {
	got := mustMarshalOpenJSONRows(t, openJSONFixtureRows())
	var doc []map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("json.Unmarshal(openJSONRows fixture): %v (output: %s)", err, got)
	}
	if len(doc) != 2 {
		t.Fatalf("decoded %d rows, want 2", len(doc))
	}
}
