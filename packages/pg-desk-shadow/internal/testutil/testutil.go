// Package testutil holds helpers shared by the module's tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
)

// RequireSQLite skips (or, with PG_DESK_SHADOW_REQUIRE_TOOLS=1, fails) when
// the sqlite3 command is unavailable.
func RequireSQLite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(sqlite.Bin); err != nil {
		if os.Getenv("PG_DESK_SHADOW_REQUIRE_TOOLS") == "1" {
			t.Fatalf("sqlite3 is required: %v", err)
		}
		t.Skip("sqlite3 not available")
	}
}

// Schema is the subset of the version-2 pg-desk store the tool reads or writes.
const Schema = `
CREATE TABLE entity (repo TEXT NOT NULL, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, facts TEXT NOT NULL, as_of TEXT NOT NULL,
  stale INTEGER NOT NULL DEFAULT 0, content_hash TEXT NOT NULL DEFAULT '', head_sha TEXT, version INTEGER NOT NULL DEFAULT 0,
  hydrated_at TEXT, active INTEGER NOT NULL DEFAULT 1, list_fp TEXT, PRIMARY KEY (repo, entity_type, entity_id));
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE consumer (name TEXT NOT NULL, type TEXT NOT NULL, cursor INTEGER NOT NULL DEFAULT 0, seen_at TEXT, PRIMARY KEY (name, type));
CREATE TABLE change_log (seq INTEGER PRIMARY KEY AUTOINCREMENT, repo TEXT NOT NULL, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL,
  version INTEGER NOT NULL, kinds TEXT NOT NULL, origin TEXT NOT NULL, at TEXT NOT NULL);
`

// FakeStore creates a store with the schema at path.
func FakeStore(t *testing.T, path string) sqlite.DB {
	t.Helper()
	RequireSQLite(t)
	db := sqlite.DB{Path: path}
	if err := db.Exec(context.Background(), Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// FactsJSON renders the pr_show part of a facts value.
func FactsJSON(head, state string, comments int) string {
	return fmt.Sprintf(`{"pr_show":{"head_sha":%q,"state":%q,"updated_at":"2026-01-05T10:00:00Z","comment_count":%d,"review_count":0,"review_decision":"","checks_rollup":"SUCCESS","merge_state_status":"CLEAN","mergeable":"MERGEABLE","connections":{"threads":{"total":0},"comments":{"total":%d},"reviews":{"total":0}},"draft":false,"label_count":1,"age_seconds":99,"as_of":"x"},"as_of":"x"}`, head, state, comments, comments)
}
