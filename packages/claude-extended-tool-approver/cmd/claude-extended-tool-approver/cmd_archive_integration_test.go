//go:build integration

// Integration coverage for the `archive` subcommand (bd pg2-riwdh). Like
// cmd_evaluate_integration_test.go, this file drives a REAL SQLite ask log
// (via asklog.NewStore/asklog.NewReadOnlyStore) and is therefore tagged
// `integration` for the same reason: fsync latency during store creation is
// a host property that can blow the default `go test` budget, and the
// package build for a nixosConfiguration only ever runs the untagged
// suite. See that file's header for the full write-up.
//
// EVERY database here is a THROWAWAY file under t.TempDir() — never the
// real, live corpus at ~/.local/share/claude-extended-tool-approver/asks.db
// (bd pg2-riwdh's hard safety constraint).

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// seedIntegrationRow inserts one tool_decisions row directly, mirroring
// cmd_evaluate_integration_test.go / internal/asklog's own test helpers.
func seedIntegrationRow(t *testing.T, store *asklog.Store, id int, createdAt string, correctDecision, correctExplanation *string) {
	t.Helper()
	hash := fmt.Sprintf("h%d", id)
	var err error
	if correctDecision == nil {
		_, err = store.DB().Exec(`INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at)
			VALUES (?, 'sess1', '/tmp', 'Bash', ?, '{"command":"ls"}', 'ls', 'allow', 'approved', ?)`,
			id, hash, createdAt)
	} else {
		_, err = store.DB().Exec(`INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at, correct_hook_decision, correct_hook_decision_explanation)
			VALUES (?, 'sess1', '/tmp', 'Bash', ?, '{"command":"rm -rf /"}', 'rm -rf /', 'allow', 'approved', ?, ?, ?)`,
			id, hash, createdAt, *correctDecision, *correctExplanation)
	}
	if err != nil {
		t.Fatalf("seed row id=%d: %v", id, err)
	}
}

// TestArchive_DryRun_WritesNothing proves the safe default: without --yes,
// the command reports candidates/sample but leaves the fixtures file
// unwritten and every row in place.
func TestArchive_DryRun_WritesNothing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")
	fixturesOut := filepath.Join(dir, "fixtures.json")

	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedIntegrationRow(t, store, 1, "2026-01-01T00:00:00Z", nil, nil)
	seedIntegrationRow(t, store, 2, "2026-09-01T00:00:00Z", nil, nil)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout, stderr, err := runCLI(t, "archive", "--db", dbPath, "--before", "2026-06-01T00:00:00Z", "--dry-run")
	if err != nil {
		t.Fatalf("archive --dry-run: %v\nstderr: %s", err, stderr)
	}
	if !containsAll(stdout, []string{"candidate rows:      1", "dry run"}) {
		t.Errorf("dry-run output missing expected lines:\n%s", stdout)
	}

	if _, statErr := os.Stat(fixturesOut); statErr == nil {
		t.Error("dry-run wrote a fixtures file; it must not")
	}

	verify, err := asklog.NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = verify.Close() }()
	rows, err := verify.QueryRows("")
	if err != nil {
		t.Fatalf("QueryRows: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("rows after dry-run = %d, want 2 (nothing deleted)", len(rows))
	}
}

// TestArchive_WithoutYes_RefusesToRun pins that omitting --yes on a
// non-dry-run invocation is an error, not a silent dry run and not a real
// delete — the command must say so rather than guessing what the operator
// meant.
func TestArchive_WithoutYes_RefusesToRun(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")
	fixturesOut := filepath.Join(dir, "fixtures.json")

	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedIntegrationRow(t, store, 1, "2026-01-01T00:00:00Z", nil, nil)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, stderr, err := runCLI(t, "archive", "--db", dbPath, "--before", "2026-06-01T00:00:00Z", "--fixtures-out", fixturesOut)
	if err == nil {
		t.Fatal("archive without --yes and without --dry-run: want a non-zero exit, got success")
	}
	if !containsAll(stderr, []string{"--yes"}) {
		t.Errorf("stderr does not explain the missing --yes:\n%s", stderr)
	}
	if _, statErr := os.Stat(fixturesOut); statErr == nil {
		t.Error("refused run wrote a fixtures file; it must not")
	}
}

// TestArchive_RealRun_SamplesDeletesAndVacuums is the full ceremony,
// end to end: fixtures are captured (with the ground-truth annotation
// intact) BEFORE the matching rows are deleted, only the old row is
// removed, and VACUUM leaves the database internally consistent.
func TestArchive_RealRun_SamplesDeletesAndVacuums(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")
	fixturesOut := filepath.Join(dir, "fixtures.json")

	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	deny := "deny"
	explanation := "should have been refused — regression fixture"
	seedIntegrationRow(t, store, 1, "2026-01-01T00:00:00Z", &deny, &explanation) // old, annotated miss
	seedIntegrationRow(t, store, 2, "2026-09-01T00:00:00Z", nil, nil)            // new, survives
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout, stderr, err := runCLI(t, "archive",
		"--db", dbPath,
		"--before", "2026-06-01T00:00:00Z",
		"--fixtures-out", fixturesOut,
		"--yes")
	if err != nil {
		t.Fatalf("archive --yes: %v\nstderr: %s", err, stderr)
	}
	if !containsAll(stdout, []string{"deleted 1 row", "VACUUM complete"}) {
		t.Errorf("archive output missing expected lines:\n%s", stdout)
	}

	// Fixtures file: the archived row must be present with its ground-truth
	// annotation intact.
	raw, err := os.ReadFile(fixturesOut)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtureSet archiveFixtureSet
	if err := json.Unmarshal(raw, &fixtureSet); err != nil {
		t.Fatalf("unmarshal fixtures: %v\nraw: %s", err, raw)
	}
	if fixtureSet.TotalCandidateRows != 1 {
		t.Errorf("TotalCandidateRows = %d, want 1", fixtureSet.TotalCandidateRows)
	}
	if len(fixtureSet.Fixtures) != 1 {
		t.Fatalf("len(Fixtures) = %d, want 1", len(fixtureSet.Fixtures))
	}
	f := fixtureSet.Fixtures[0]
	if f.ID != 1 {
		t.Errorf("fixture ID = %d, want 1", f.ID)
	}
	if f.CorrectHookDecision == nil || *f.CorrectHookDecision != deny {
		t.Errorf("fixture CorrectHookDecision = %v, want %q — ground truth must survive into the fixture", f.CorrectHookDecision, deny)
	}
	if f.CorrectHookDecisionExplanation == nil || *f.CorrectHookDecisionExplanation != explanation {
		t.Errorf("fixture CorrectHookDecisionExplanation = %v, want %q", f.CorrectHookDecisionExplanation, explanation)
	}

	// The database itself: only row 2 (the new one) must remain.
	verify, err := asklog.NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = verify.Close() }()
	rows, err := verify.QueryRows("")
	if err != nil {
		t.Fatalf("QueryRows: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 2 {
		t.Fatalf("rows after archive = %+v, want exactly row id=2", rows)
	}

	var integrityResult string
	if err := verify.DB().QueryRow("PRAGMA integrity_check").Scan(&integrityResult); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrityResult != "ok" {
		t.Errorf("integrity_check after archive = %q, want ok", integrityResult)
	}
}

// TestArchive_NoCandidates_SkipsEverything: when nothing is older than the
// threshold, archive must not write a fixtures file, delete anything, or
// run VACUUM — there is nothing for any of those steps to do.
func TestArchive_NoCandidates_SkipsEverything(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")
	fixturesOut := filepath.Join(dir, "fixtures.json")

	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedIntegrationRow(t, store, 1, "2026-09-01T00:00:00Z", nil, nil)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout, stderr, err := runCLI(t, "archive",
		"--db", dbPath,
		"--before", "2026-01-01T00:00:00Z",
		"--fixtures-out", fixturesOut,
		"--yes")
	if err != nil {
		t.Fatalf("archive with no candidates: %v\nstderr: %s", err, stderr)
	}
	if !containsAll(stdout, []string{"nothing older than the threshold"}) {
		t.Errorf("stdout missing the nothing-to-archive message:\n%s", stdout)
	}
	if _, statErr := os.Stat(fixturesOut); statErr == nil {
		t.Error("no-candidates run wrote a fixtures file; it must not")
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
