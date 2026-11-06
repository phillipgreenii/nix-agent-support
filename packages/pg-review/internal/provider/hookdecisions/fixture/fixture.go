// Package fixture builds a SYNTHETIC decision database for tests. The table
// mirrors the approval hook's tool_decisions schema (its migrations applied in
// order); nothing here is a real decision, host or path.
package fixture

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const ddl = `CREATE TABLE tool_decisions (
	id                     INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id             TEXT NOT NULL,
	cwd                    TEXT NOT NULL,
	agent_id               TEXT,
	agent_type             TEXT,
	tool_name              TEXT NOT NULL,
	tool_use_id            TEXT,
	tool_input_hash        TEXT NOT NULL,
	tool_input_json        TEXT NOT NULL,
	tool_summary           TEXT,
	hook_decision          TEXT,
	hook_reason            TEXT,
	permission_suggestions TEXT,
	outcome                TEXT NOT NULL DEFAULT 'pending',
	outcome_notes          TEXT,
	created_at             TEXT NOT NULL,
	resolved_at            TEXT,
	excluded               INTEGER NOT NULL DEFAULT 0,
	excluded_reason        TEXT,
	correct_hook_decision  TEXT,
	correct_hook_decision_explanation TEXT,
	sandbox_enabled        INTEGER,
	permission_mode        TEXT,
	prompt_id              TEXT,
	tool_response          TEXT,
	transcript_path        TEXT
)`

// Decision is one synthetic tool_decisions row.
type Decision struct {
	Session  string
	CWD      string
	Tool     string
	Command  string // Bash command; empty for non-Bash tools
	Outcome  string
	At       string // RFC3339 UTC, as the hook stores it
	Excluded bool
}

// Build creates the database in a temp dir and returns its path.
func Build(t testing.TB, rows []Decision) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "asks.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		in := map[string]string{}
		if r.Command != "" {
			in["command"] = r.Command
		} else {
			in["file_path"] = "relative/file.txt"
		}
		b, _ := json.Marshal(in)
		_, err := db.Exec(`INSERT INTO tool_decisions
			(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at, excluded)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Session, r.CWD, r.Tool, "h"+string(rune('a'+i%26)), string(b), r.Outcome, r.At, r.Excluded)
		if err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// Standard is the fixture the golden test and the CLI tests share. All
// directories are synthetic; sessions s1..s4 are fabricated ids.
func Standard() []Decision {
	const w = "/work/proj"
	return []Decision{
		{"s1", w + "/a", "Bash", "git push origin main", "rejected", "2026-03-01T10:00:00Z", false},
		{"s2", w + "/a", "Bash", "git push --force", "rejected", "2026-03-02T10:00:00Z", false},
		{"s1", w + "/b", "Bash", "FOO=1 git status", "rejected", "2026-03-03T10:00:00Z", false},
		{"s3", w + "/b", "Bash", "/usr/bin/curl http://example.invalid", "rejected", "2026-03-03T11:00:00Z", false},
		{"s3", w + "/b", "Bash", "curl http://example.invalid/x", "denied", "2026-03-04T09:00:00Z", false},
		{"s2", w + "/a", "Write", "", "failed", "2026-03-04T10:00:00Z", false},
		{"s4", "/neutral/review", "Bash", "git push origin main", "rejected", "2026-03-04T12:00:00Z", false},
		{"s4", "/other/place", "Bash", "git push origin main", "rejected", "2026-03-05T12:00:00Z", false},
		{"s1", w + "/a", "Bash", "git push origin main", "approved", "2026-03-05T13:00:00Z", false},
		{"s1", w + "/a", "Bash", "git push origin dev", "rejected", "2026-02-01T10:00:00Z", false},
		{"s1", w + "/a", "Bash", "git push origin dev", "rejected", "2026-03-06T10:00:00Z", true},
		{"s1", w + "/a", "Bash", "git push origin dev", "rejected", "2026-03-10T00:00:00Z", false},
	}
}
