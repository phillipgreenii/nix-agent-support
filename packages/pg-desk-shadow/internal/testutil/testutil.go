// Package testutil holds helpers shared by the module's tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
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

// RequireJQ returns the directory of the jq executable, skipping (or, with
// PG_DESK_SHADOW_REQUIRE_TOOLS=1, failing) when it is unavailable. The
// collector's stand-in client script reads its request with jq.
func RequireJQ(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("jq")
	if err != nil {
		if os.Getenv("PG_DESK_SHADOW_REQUIRE_TOOLS") == "1" {
			t.Fatalf("jq is required: %v", err)
		}
		t.Skip("jq not available")
	}
	return filepath.Dir(p)
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

// NewRunner returns an UNSANDBOXED runner whose environment passes
// safety.Verify against a throwaway scratch policy (no daemon variables), for
// tests that run stand-in executables. It never touches the real sandbox, the
// real state directories or the network.
func NewRunner(t *testing.T) *runner.Runner {
	t.Helper()
	root := safety.Resolve(t.TempDir())
	home := safety.Resolve(t.TempDir())
	p := safety.Policy{
		Scratch: root, StateHome: filepath.Join(root, "state"), RuntimeDir: filepath.Join(root, "run"), TmpDir: filepath.Join(root, "tmp"),
		BinDir: filepath.Join(root, "bin"), DeskConfig: filepath.Join(root, "config", "pg-desk.yaml"), PRConfig: filepath.Join(root, "config", "pg-pr.yaml"),
		Home: home, LiveRoots: []string{filepath.Join(home, ".local", "state")},
	}
	for _, d := range []string{p.StateHome, p.RuntimeDir, p.TmpDir, p.BinDir, filepath.Dir(p.PRConfig)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(p.PRConfig, []byte("connector: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &runner.Runner{Policy: p, Env: safety.ChildEnv(p, "/usr/bin:/bin"), Unsandboxed: true, WorkDir: root}
}

// WriteScript writes an executable shell script named name under dir and
// returns its path.
func WriteScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// ProcessGone polls up to two seconds for pid to no longer exist (a zombie
// awaiting reaping counts as existing) and reports whether it is gone.
func ProcessGone(pid int) bool {
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
