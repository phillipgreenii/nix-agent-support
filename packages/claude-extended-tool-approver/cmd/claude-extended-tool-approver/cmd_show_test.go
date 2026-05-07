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
	store.Close()
	return dir
}

func runSubcommand(t *testing.T, dataDir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
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
	store.Close()
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
