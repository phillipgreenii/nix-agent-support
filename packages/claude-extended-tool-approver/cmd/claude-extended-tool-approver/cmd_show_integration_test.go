//go:build integration

// Integration tests for the `show`, `evaluate` and `report` subcommands. Every
// test here seeds a real SQLite ask log and drives the COMPILED binary over it
// (runSubcommand -> exec.Command(testBinary(t))), so all of them carry the
// `integration` tag. Measured 2026-08-16, they ranged 17.57s-52.06s apiece on a
// host whose array had lost its write cache. See main_integration_test.go for
// the full rationale and for how to run them:
//
//	go test -tags integration ./...

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

func setupShowTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "claude-extended-tool-approver", "asks.db")
	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (1, 'sess1', '/tmp', 'Bash', 'h1', '{"command":"git log"}', 'git log', 'allow', 'git: read-only', 'approved', '2026-03-01T00:00:00Z')`,
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (2, 'sess1', '/tmp', 'Bash', 'h2', '{"command":"rm -rf /"}', 'rm -rf /', 'deny', 'dangerous', 'denied', '2026-03-01T00:00:00Z')`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Close()
	return dir
}

func runSubcommand(t *testing.T, dataDir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(testBinary(t), args...)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dataDir)
	return cmd.CombinedOutput()
}

func TestShow_JSONFormat(t *testing.T) {
	dataDir := setupShowTestDB(t)
	out, err := runSubcommand(t, dataDir, "show", "--format=json", "1", "2")
	if err != nil {
		t.Fatalf("show failed: %v\noutput: %s", err, out)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(out), &rows); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["tool_summary"] != "git log" {
		t.Errorf("row 0 tool_summary = %v, want 'git log'", rows[0]["tool_summary"])
	}
	if rows[0]["tool_input_json"] == nil || rows[0]["tool_input_json"] == "" {
		t.Error("row 0 tool_input_json should be populated")
	}
}

func TestShow_TableFormat(t *testing.T) {
	dataDir := setupShowTestDB(t)
	out, err := runSubcommand(t, dataDir, "show", "1")
	if err != nil {
		t.Fatalf("show failed: %v\noutput: %s", err, out)
	}
	if !bytes.Contains(out, []byte("git log")) {
		t.Errorf("table output should contain 'git log', got: %s", string(out))
	}
}

func TestShow_NoIDs(t *testing.T) {
	dataDir := setupShowTestDB(t)
	out, err := runSubcommand(t, dataDir, "show")
	if err == nil {
		t.Fatalf("show with no IDs should fail, got: %s", out)
	}
}

func setupShowTestDBWithTrace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "claude-extended-tool-approver", "asks.db")
	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (1, 'sess1', '/tmp', 'Bash', 'h1', '{"command":"git push --force"}', 'git push --force', 'ask', 'force push', 'pending', '2026-03-01T00:00:00Z')`,
		`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason)
		 VALUES (1, 1, 'envvars', 'abstain', 'not relevant')`,
		`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason)
		 VALUES (1, 2, 'git', 'ask', 'force push detected')`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Close()
	return dir
}

func TestShow_JSONWithTrace(t *testing.T) {
	dataDir := setupShowTestDBWithTrace(t)
	out, err := runSubcommand(t, dataDir, "show", "--format=json", "1")
	if err != nil {
		t.Fatalf("show failed: %v\noutput: %s", err, out)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(out), &rows); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	trace, ok := rows[0]["trace"].([]interface{})
	if !ok {
		t.Fatalf("trace field missing or not array: %v", rows[0]["trace"])
	}
	if len(trace) != 2 {
		t.Fatalf("trace has %d entries, want 2", len(trace))
	}
}

func TestShow_TableWithTrace(t *testing.T) {
	dataDir := setupShowTestDBWithTrace(t)
	out, err := runSubcommand(t, dataDir, "show", "1")
	if err != nil {
		t.Fatalf("show failed: %v\noutput: %s", err, out)
	}
	output := string(out)
	if !strings.Contains(output, "TRACE:") {
		t.Errorf("table output should contain TRACE section, got: %s", output)
	}
	if !strings.Contains(output, "envvars") {
		t.Errorf("table output should contain rule name 'envvars', got: %s", output)
	}
}

func TestEvaluate_JSONIncludesToolSummary(t *testing.T) {
	dataDir := setupShowTestDB(t)
	out, err := runSubcommand(t, dataDir, "evaluate", "--format=json")
	if err != nil {
		t.Fatalf("evaluate failed: %v\noutput: %s", err, out)
	}
	// The engine may emit log lines to stderr which CombinedOutput mixes in.
	// Strip any non-JSON prefix lines before the opening '['.
	jsonStart := bytes.IndexByte(out, '[')
	if jsonStart < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	jsonOut := bytes.TrimSpace(out[jsonStart:])
	var results []map[string]interface{}
	if err := json.Unmarshal(jsonOut, &results); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	for _, r := range results {
		if _, ok := r["tool_summary"]; !ok {
			t.Errorf("result id=%v missing tool_summary field", r["id"])
		}
	}
}

// setupCommandGroupingDB inserts two rows for the SAME underlying command, one
// written with && and one written across a newline. They must share a
// command_class / report group (bead pg2-okd13.3). cwd is /tmp so evaluate's
// engine replay does not classify them as stale-cwd.
func setupCommandGroupingDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "claude-extended-tool-approver", "asks.db")
	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (1, 'sess1', '/tmp', 'Bash', 'h1', '{"command":"cd /tmp && ls -la"}', 'cd /tmp && ls -la', 'allow', 'r', 'approved', '2026-03-01T00:00:00Z')`,
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (2, 'sess1', '/tmp', 'Bash', 'h2', '{"command":"cd /tmp\nls -la"}', 'cd /tmp', 'allow', 'r', 'approved', '2026-03-01T00:00:00Z')`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Close()
	return dir
}

func TestEvaluate_JSONIncludesCommandClass(t *testing.T) {
	dataDir := setupCommandGroupingDB(t)
	out, err := runSubcommand(t, dataDir, "evaluate", "--format=json")
	if err != nil {
		t.Fatalf("evaluate failed: %v\noutput: %s", err, out)
	}
	jsonStart := bytes.IndexByte(out, '[')
	if jsonStart < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	var results []map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(out[jsonStart:]), &results); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2\nraw: %s", len(results), out)
	}
	for _, r := range results {
		cc, ok := r["command_class"].(string)
		if !ok || cc == "" {
			t.Errorf("result id=%v missing command_class field", r["id"])
		}
	}
	// The multiline row must bucket by its real tail command, not its first
	// line — so it shares the compound row's command_class.
	if results[0]["command_class"] != results[1]["command_class"] {
		t.Errorf("multiline and compound rows should share command_class: %v vs %v",
			results[0]["command_class"], results[1]["command_class"])
	}
	if cc := results[1]["command_class"].(string); cc == "cd /tmp" {
		t.Errorf("multiline row command_class %q must not be the newline-truncated first line", cc)
	}
}

func TestReport_GroupByCommand_MergesMultilineAndCompound(t *testing.T) {
	dataDir := setupCommandGroupingDB(t)
	out, err := runSubcommand(t, dataDir, "report", "--group-by=command", "--format=json")
	if err != nil {
		t.Fatalf("report failed: %v\noutput: %s", err, out)
	}
	jsonStart := bytes.IndexByte(out, '[')
	if jsonStart < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(out[jsonStart:]), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	// Both rows collapse to ONE normalized command class.
	if len(entries) != 1 {
		t.Fatalf("want 1 group, got %d\nraw: %s", len(entries), out)
	}
	if entries[0]["count"] != float64(2) {
		t.Errorf("want count 2, got %v", entries[0]["count"])
	}
	key, _ := entries[0]["key"].(string)
	if key == "cd /tmp" {
		t.Errorf("group key %q must not be the newline-truncated first line", key)
	}
	if !strings.Contains(key, "ls -la") {
		t.Errorf("group key %q should reflect the real tail command", key)
	}
}
