package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// serve on a state directory whose index.db has an older schema_version refuses
// to start (it does not begin a rebuild that runs for hours): exit 20, a
// schema_rebuild_required log event and a message naming the rebuild command,
// and index.db is left untouched.
func TestServeRefusesAnOlderIndexSchemaAndNamesTheRebuildCommand(t *testing.T) {
	k := testkit.New(t)
	if err := k.Store.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(k.Dir, store.DBName)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE meta SET value=? WHERE key='schema_version'`, fmt.Sprint(store.SchemaVersion-1)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(shortDir(t), "c.toml")
	cfg := fmt.Sprintf("stateDir = %q\nclaudeDir = %q\nsocket = %q\n", k.Dir, testkit.ClaudeDir, filepath.Join(shortDir(t), "r.sock"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _, errw := testApp(map[string]string{config.EnvConfig: cfgPath})
	if code := a.run([]string{"serve"}); code != wire.ExitSchemaRebuild {
		t.Fatalf("exit %d, want %d: %s", code, wire.ExitSchemaRebuild, errw.String())
	}
	var ev map[string]any
	for _, m := range jsonLogLines(t, errw.String()) {
		if m["event"] == "schema_rebuild_required" {
			ev = m
		}
		if m["event"] == "open_failed" {
			t.Fatalf("generic open_failed logged: %v", m)
		}
	}
	if ev == nil || ev["command"] != "pg-claude-code-recorder rebuild" || ev["found"].(float64) != float64(store.SchemaVersion-1) || ev["known"].(float64) != float64(store.SchemaVersion) {
		t.Fatalf("event %v in %s", ev, errw.String())
	}
	if !strings.Contains(errw.String(), "error: schema_rebuild_required\n") || !strings.Contains(errw.String(), `run "pg-claude-code-recorder rebuild"`) {
		t.Fatalf("stderr %q", errw.String())
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("serve changed index.db: %v", err)
	}
	if _, err := os.Stat(dbPath + ".pre-rebuild"); err == nil {
		t.Fatal("serve began a rebuild")
	}
}
