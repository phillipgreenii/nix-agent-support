package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// This file pins the `pg-desk open --json` machine-readable contract, ported
// from packages/pg-pr/cmd/pg-pr/open_json_test.go: the top-level payload is a
// BARE JSON ARRAY of row objects, never an enveloped object, so an empty
// selection is a well-defined empty array.

func jsonRows(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a bare JSON array of objects: %v\nstdout: %s", err, stdout)
	}
	return rows
}

func TestOpenJSONMode_Schema(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	seedOpenPR(t, st, "o/r", 1, "one", "https://example.test/pull/1", "alice", "team", panelTeamAwaitingMe, 1, []string{"review-requested"}, false, false)

	stdout, _, err := runOpenCmd(t, openFlags{all: true, jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}

	rows := jsonRows(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	wantKeys := []string{
		"number", "owner", "title", "url", "ci_status",
		"human_approvers", "agent_approvers", "files_changed", "lines_changed",
		"needs_attention", "match_reason", "hidden", "hidden_reason",
		"ready_to_promote", "truncated",
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

func TestOpenJSONMode_EmptySelectionIsEmptyArray(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	stdout, _, err := runOpenCmd(t, openFlags{jsonOutput: true})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want a bare empty JSON array", stdout)
	}
}

func TestOpenJSONMode_MaxTruncationIsVisible(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	for i := 1; i <= 3; i++ {
		seedOpenPR(t, st, "o/r", i, "t", "https://example.test/pull/"+numToID(i), "alice", "team", panelTeamAwaitingMe, 0, []string{"review-requested"}, false, false)
	}

	stdout, stderr, err := runOpenCmd(t, openFlags{all: true, max: 2, jsonOutput: true})
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
	if !strings.Contains(stderr, "--max") {
		t.Errorf("stderr does not still report the truncation: %q", stderr)
	}
}

func TestOpenJSONMode_PGDeskOutputEnvSelectsJSON(t *testing.T) {
	t.Setenv(outputEnvVar, "json")

	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	seedOpenPR(t, st, "o/r", 1, "one", "https://example.test/pull/1", "alice", "team", panelTeamAwaitingMe, 0, []string{"review-requested"}, false, false)

	stdout, _, err := runOpenCmd(t, openFlags{all: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	rows := jsonRows(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
}
