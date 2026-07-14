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
	defer func() { _ = db.Close() }()
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

// TestMigrateRuntimeJSON_NewShapeFileLeftUntouched is the regression guard for
// the toggle-clobber bug. A runtime.json carrying ONLY nudger state (the
// post-migration shape, no caffeinate_on/auto_resume_enabled keys) is NOT a
// legacy file: the migration must not write the toggle store (which would zero
// the user's real DB values) and must not delete the file (which holds live
// nudger state). The WatermarkStore re-creates this file on every run, so the
// migration encounters it on every boot — clobbering toggles to false each time.
func TestMigrateRuntimeJSON_NewShapeFileLeftUntouched(t *testing.T) {
	dir := t.TempDir()
	rtPath := filepath.Join(dir, "runtime.json")

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("sqlite.Migrate: %v", err)
	}
	ts := sqlite.NewToggleStore(db)
	ctx := context.Background()

	// The user's real, persisted toggle values live in the DB.
	if err := ts.Set(ctx, "caffeinate_on", true); err != nil {
		t.Fatal(err)
	}
	if err := ts.Set(ctx, "auto_resume_enabled", true); err != nil {
		t.Fatal(err)
	}

	// A new-shape runtime.json: nudger state only, no toggle keys.
	if err := os.WriteFile(rtPath, []byte(`{"nudger":{"window_reset_fired_for":"0001-01-01T00:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MigrateRuntimeJSON(ctx, rtPath, ts, sqlite.NewNudgeStore(db), sqlite.NewSessionStore(db)); err != nil {
		t.Fatalf("MigrateRuntimeJSON: %v", err)
	}

	// DB toggles must be UNCHANGED (not clobbered to false).
	if v, _, _ := ts.Get(ctx, "caffeinate_on"); !v {
		t.Error("caffeinate_on was clobbered to false by migration of a new-shape file")
	}
	if v, _, _ := ts.Get(ctx, "auto_resume_enabled"); !v {
		t.Error("auto_resume_enabled was clobbered to false by migration of a new-shape file")
	}

	// The nudger-state file must NOT be deleted (it holds live nudger state).
	if _, err := os.Stat(rtPath); err != nil {
		t.Errorf("new-shape runtime.json was deleted by migration: %v", err)
	}
}

func TestMigrateRuntimeJSON_MissingFile_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	rtPath := filepath.Join(dir, "runtime.json")

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
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
	defer func() { _ = db.Close() }()
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
