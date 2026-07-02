package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// migrateStoreOpen is injected by tests to point at a temp store.
// Production code sets it to store.Open.
// (defined in migrate_cmd.go, tested here)

func resetMigrateCmdFlags() {
	mcF = migrateCmdFlags{}
}

func TestMigrateCmd_RunsAllMigrations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = db.Close()

	prev := migrateStoreOpen
	t.Cleanup(func() { migrateStoreOpen = prev })
	migrateStoreOpen = func(path string) (*store.DB, error) { return store.Open(dbPath) }

	resetMigrateCmdFlags()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate", "--db", dbPath})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate: %v (stderr=%s)", err, stderr.String())
	}

	out := stdout.String()
	// The command must print some indication of what ran.
	if !strings.Contains(out, "migrate") && !strings.Contains(out, "ok") && !strings.Contains(out, "applied") {
		t.Errorf("expected migration output; got: %q", out)
	}
}

func TestMigrateCmd_DryRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-dry.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = db.Close()

	prev := migrateStoreOpen
	t.Cleanup(func() { migrateStoreOpen = prev })
	migrateStoreOpen = func(path string) (*store.DB, error) { return store.Open(dbPath) }

	resetMigrateCmdFlags()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate", "--db", dbPath, "--dry-run"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate --dry-run: %v (stderr=%s)", err, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected 'dry-run' in output; got: %q", out)
	}
}

func TestMigrateCmd_AlreadyUpToDate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-uptodate.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Run migrations once to mark all applied.
	ctx := context.Background()
	if err := store.RunDataMigrations(ctx, db, store.RegisteredDataMigrations); err != nil {
		t.Fatalf("seed migrations: %v", err)
	}
	_ = db.Close()

	prev := migrateStoreOpen
	t.Cleanup(func() { migrateStoreOpen = prev })
	migrateStoreOpen = func(path string) (*store.DB, error) { return store.Open(dbPath) }

	resetMigrateCmdFlags()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate", "--db", dbPath})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate (already up to date): %v (stderr=%s)", err, stderr.String())
	}

	out := stdout.String()
	// Should say nothing to do, up to date, or 0 applied.
	if !strings.Contains(out, "up-to-date") && !strings.Contains(out, "nothing") && !strings.Contains(out, "0") {
		t.Errorf("expected up-to-date message; got: %q", out)
	}
}
