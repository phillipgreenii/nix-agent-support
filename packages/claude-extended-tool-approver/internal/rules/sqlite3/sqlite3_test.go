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
		{"insert on readonly db", `sqlite3 /nix/store/abc/test.db "INSERT INTO t VALUES(1)"`, hookio.Abstain},
		{"create table", `sqlite3 /tmp/project/test.db "CREATE TABLE t(id INT)"`, hookio.Abstain},
		{"drop table", `sqlite3 /tmp/project/test.db "DROP TABLE t"`, hookio.Abstain},
		{"select on unknown path", `sqlite3 /home/other/test.db "SELECT 1"`, hookio.Abstain},
		{"select with json flag", `sqlite3 -json /tmp/project/test.db "SELECT 1"`, hookio.Approve},
		{"dot-command schema", `sqlite3 /tmp/project/test.db ".schema"`, hookio.Approve},
		{"dot-command tables", `sqlite3 /tmp/project/test.db ".tables"`, hookio.Approve},
		{"dot-command headers on", `sqlite3 /tmp/project/test.db ".headers on"`, hookio.Approve},
		{"dot-command mode", `sqlite3 /tmp/project/test.db ".mode json"`, hookio.Approve},
		{"dot-command dbinfo", `sqlite3 /tmp/project/test.db ".dbinfo"`, hookio.Approve},
		{"dot-command schema on nix store", `sqlite3 /nix/store/abc/test.db ".schema"`, hookio.Approve},
		{"dot-command on unknown path", `sqlite3 /home/other/test.db ".schema"`, hookio.Abstain},
		{"pragma unknown", `sqlite3 /tmp/project/test.db "PRAGMA table_info(t)"`, hookio.Abstain},
		{"not sqlite3", "ls -la", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
				CWD:       "/tmp/project",
			}
			got := r.Evaluate(input)
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
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
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
