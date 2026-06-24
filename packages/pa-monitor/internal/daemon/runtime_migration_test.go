package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

func TestMigrateRuntimeJSON_PopulatesToggles(t *testing.T) {
	dir := t.TempDir()
	rtPath := filepath.Join(dir, "runtime.json")

	// Write a legacy (pre-migration) runtime.json carrying the toggle keys:
	// caffeinate_on=true, auto_resume_enabled=false.
	if err := os.WriteFile(rtPath, []byte(`{"caffeinate_on":true,"auto_resume_enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("sqlite.Migrate: %v", err)
	}
	ts := sqlite.NewToggleStore(db)

	if err := MigrateRuntimeJSON(context.Background(), rtPath, ts, sqlite.NewNudgeStore(db), sqlite.NewSessionStore(db)); err != nil {
		t.Fatalf("MigrateRuntimeJSON: %v", err)
	}

	caff, present, err := ts.Get(context.Background(), "caffeinate_on")
	if err != nil {
		t.Fatalf("Get caffeinate_on: %v", err)
	}
	if !present || !caff {
		t.Errorf("caffeinate_on = (%v, %v), want (true, true)", caff, present)
	}

	autoResume, present, err := ts.Get(context.Background(), "auto_resume_enabled")
	if err != nil {
		t.Fatalf("Get auto_resume_enabled: %v", err)
	}
	if !present || autoResume {
		t.Errorf("auto_resume_enabled = (%v, %v), want (false, true)", autoResume, present)
	}

	// runtime.json must be deleted after migration.
	if _, err := os.Stat(rtPath); !os.IsNotExist(err) {
		t.Errorf("runtime.json still exists post-migration")
	}

	_ = time.Time{} // keep time import active for future nudge tests
}

func TestMigrateRuntimeJSON_MissingFile_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	rtPath := filepath.Join(dir, "runtime.json")

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("sqlite.Migrate: %v", err)
	}
	ts := sqlite.NewToggleStore(db)

	// Should return nil even though runtime.json does not exist.
	if err := MigrateRuntimeJSON(context.Background(), rtPath, ts, sqlite.NewNudgeStore(db), sqlite.NewSessionStore(db)); err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}

	all, err := ts.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty toggles, got %v", all)
	}
}

func TestMigrateRuntimeJSON_Idempotent_AfterDelete(t *testing.T) {
	dir := t.TempDir()
	rtPath := filepath.Join(dir, "runtime.json")

	if err := os.WriteFile(rtPath, []byte(`{"caffeinate_on":true,"auto_resume_enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("sqlite.Migrate: %v", err)
	}
	ts := sqlite.NewToggleStore(db)

	ctx := context.Background()
	ns := sqlite.NewNudgeStore(db)
	ss := sqlite.NewSessionStore(db)

	// First call: migrates and deletes runtime.json.
	if err := MigrateRuntimeJSON(ctx, rtPath, ts, ns, ss); err != nil {
		t.Fatalf("first MigrateRuntimeJSON: %v", err)
	}
	// Second call: runtime.json is gone, must be a no-op.
	if err := MigrateRuntimeJSON(ctx, rtPath, ts, ns, ss); err != nil {
		t.Fatalf("second MigrateRuntimeJSON (idempotent): %v", err)
	}
}
