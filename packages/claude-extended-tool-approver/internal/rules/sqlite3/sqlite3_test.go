package sqlite3

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestSqlite3Rule(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"select on project db", `sqlite3 /tmp/project/test.db "SELECT * FROM t"`, hookio.Approve},
		{"select on nix store db", `sqlite3 /nix/store/abc/test.db "SELECT 1"`, hookio.Approve},
		{"insert on project db", `sqlite3 /tmp/project/test.db "INSERT INTO t VALUES(1)"`, hookio.Approve},
		{"insert on readonly db", `sqlite3 /nix/store/abc/test.db "INSERT INTO t VALUES(1)"`, hookio.NoOpinion},
		{"create table", `sqlite3 /tmp/project/test.db "CREATE TABLE t(id INT)"`, hookio.NoOpinion},
		{"drop table", `sqlite3 /tmp/project/test.db "DROP TABLE t"`, hookio.NoOpinion},
		{"select on unknown path", `sqlite3 /home/other/test.db "SELECT 1"`, hookio.NoOpinion},
		{"select with json flag", `sqlite3 -json /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"dot-command schema", `sqlite3 /tmp/project/test.db ".schema"`, hookio.Approve},
		{"dot-command tables", `sqlite3 /tmp/project/test.db ".tables"`, hookio.Approve},
		{"dot-command headers on", `sqlite3 /tmp/project/test.db ".headers on"`, hookio.Approve},
		{"dot-command mode", `sqlite3 /tmp/project/test.db ".mode json"`, hookio.Approve},
		{"dot-command dbinfo", `sqlite3 /tmp/project/test.db ".dbinfo"`, hookio.Approve},
		{"dot-command schema on nix store", `sqlite3 /nix/store/abc/test.db ".schema"`, hookio.Approve},
		{"dot-command on unknown path", `sqlite3 /home/other/test.db ".schema"`, hookio.NoOpinion},
		{"pragma unknown", `sqlite3 /tmp/project/test.db "PRAGMA table_info(t)"`, hookio.NoOpinion},
		{"not sqlite3", "ls -la", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
				CWD:       "/tmp/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestSqlite3Rule_NonBash(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/test.db"}),
		CWD:       "/tmp/project",
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("non-bash: got %v, want abstain", got.Decision)
	}
}

func TestSqlite3Rule_Name(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	if got := r.Name(); got != "sqlite3" {
		t.Errorf("Name() = %q, want sqlite3", got)
	}
}

// TestSqlite3Rule_ValueFlagsCompleteness pins pg2-33mai's ADR 0055 mode-4 fix:
// value-taking sqlite3 flags this table used to omit (confirmed against a real
// `sqlite3 --help`) made parseArgs misattribute the flag's OWN value as dbPath (or
// dbPath already set, as query), shifting both positionals so classifyQuery no
// longer saw the real query text at all. Before the fix every case below measured
// NoOpinion (the shifted query failed to classify as queryRead/queryWrite); after
// it, dbPath/query resolve correctly and the rule approves exactly as it would
// without the flag present.
func TestSqlite3Rule_ValueFlagsCompleteness(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"-nullvalue (single value)", `sqlite3 -nullvalue NULL /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-vfs (single value)", `sqlite3 -vfs unix /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-key (single value)", `sqlite3 -key secret /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-hexkey (single value)", `sqlite3 -hexkey deadbeef /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-escape (single value)", `sqlite3 -escape symbol /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-maxsize (single value)", `sqlite3 -maxsize 1000000 /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-nonce (single value)", `sqlite3 -nonce abc123 /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-textkey (single value)", `sqlite3 -textkey passphrase /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-lookaside (two values)", `sqlite3 -lookaside 1200 100 /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-pagecache (two values)", `sqlite3 -pagecache 1500 100 /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"-nullvalue before an insert on a read-only path still declines", `sqlite3 -nullvalue NULL /nix/store/abc/test.db "INSERT INTO t VALUES(1)"`, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
				CWD:       "/tmp/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}
