package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// openTestWriteService opens an in-memory SQLite DB, runs migrations, and
// returns a started WriteService backed by it. The DB is closed and the
// service stopped via t.Cleanup.
func openTestWriteService(t *testing.T) (*service.WriteService, *sqlite.SessionStore) {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("sqlite.Migrate: %v", err)
	}
	deps := service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	}
	ws := service.NewWriteService(deps)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		ws.Stop()
	})
	ws.Start(ctx)
	return ws, sqlite.NewSessionStore(db)
}

// TestGCSweeper_ReconcilesFiles verifies that RunOnce soft-deletes sessions
// whose files are absent on disk and leaves sessions with a file untouched.
func TestGCSweeper_ReconcilesFiles(t *testing.T) {
	ctx := context.Background()

	ws, sessionStore := openTestWriteService(t)

	// Insert three sessions (a, b, c).
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		if err := ws.UpsertSession(ctx, store.Session{
			SessionID:       id,
			LastProcessedAt: now,
			UpdatedAt:       now,
			CreatedAt:       now,
		}); err != nil {
			t.Fatalf("UpsertSession(%s): %v", id, err)
		}
	}
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Filesystem only has a.json.
	sessionsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionsDir, "a.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("create a.json: %v", err)
	}

	sweeper := &GCSweeper{
		SessionsDir:     sessionsDir,
		WriteService:    ws,
		Interval:        time.Hour,
		HardDeleteAfter: 24 * time.Hour,
	}
	if err := sweeper.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync after GC: %v", err)
	}

	// Session a must still be alive (file present).
	fresh := store.FreshnessWindow{Sessions: 24 * time.Hour}
	if got, err := sessionStore.GetByID(ctx, "a", fresh); err != nil {
		t.Fatalf("GetByID(a): %v", err)
	} else if got == nil {
		t.Error("session a: want alive, got nil")
	}

	// Sessions b and c must be soft-deleted (no file on disk).
	// GetByID filters out soft-deleted rows, so nil means deleted.
	if got, err := sessionStore.GetByID(ctx, "b", fresh); err != nil {
		t.Fatalf("GetByID(b): %v", err)
	} else if got != nil {
		t.Errorf("session b: want soft-deleted (nil), got %+v", got)
	}
	if got, err := sessionStore.GetByID(ctx, "c", fresh); err != nil {
		t.Fatalf("GetByID(c): %v", err)
	} else if got != nil {
		t.Errorf("session c: want soft-deleted (nil), got %+v", got)
	}
}

// TestGCSweeper_MissingDirIsNoop verifies that RunOnce returns nil and does
// not crash or touch any rows when SessionsDir does not exist.
func TestGCSweeper_MissingDirIsNoop(t *testing.T) {
	ctx := context.Background()
	ws, _ := openTestWriteService(t)

	sweeper := &GCSweeper{
		SessionsDir:     "/tmp/pamon-gc-nonexistent-dir-for-test",
		WriteService:    ws,
		HardDeleteAfter: 24 * time.Hour,
	}
	if err := sweeper.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce on missing dir: %v", err)
	}
}

// TestListSessionFiles verifies extension stripping and filtering.
func TestListSessionFiles(t *testing.T) {
	dir := t.TempDir()
	// Create files of various types.
	for _, name := range []string{"abc123.json", "def456.jsonl", "ignored.txt", "sub"} {
		p := filepath.Join(dir, name)
		if name == "sub" {
			_ = os.Mkdir(p, 0o755)
		} else {
			_ = os.WriteFile(p, []byte("{}"), 0o600)
		}
	}

	ids, err := listSessionFiles(dir)
	if err != nil {
		t.Fatalf("listSessionFiles: %v", err)
	}
	want := map[string]bool{"abc123": true, "def456": true}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing expected id %q in result %v", k, ids)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected id %q in result %v", k, ids)
		}
	}
}

// TestListSessionFiles_IgnoresStatusSiblings proves a status-line rate_limits
// sibling never derives a phantom session id (ADR 0021 §2). A <id>.status.jsonl
// (and the <id>.status.last hash sidecar) must be skipped; a genuine <id>.json
// / <id>.jsonl still yields its id.
func TestListSessionFiles_IgnoresStatusSiblings(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"real-1.json",
		"real-2.jsonl",
		"sess-1.status.jsonl", // rate_limits sibling — must NOT become "sess-1.status"
		"sess-1.status.last",  // hash sidecar — must NOT become "sess-1.status"
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := listSessionFiles(dir)
	if err != nil {
		t.Fatalf("listSessionFiles: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	want := map[string]bool{"real-1": true, "real-2": true}
	for k := range want {
		if !got[k] {
			t.Errorf("missing expected id %q in %v", k, ids)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected (phantom) id %q in %v", k, ids)
		}
	}
}
