package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// writeRegistryFile lays out one session the way Claude Code does under
// ~/.claude/sessions: a <pid>.json registry file whose sessionId field names
// the session, plus a <pid>.<hash>.key sidecar. The filename carries the PID,
// never the session id (bead pg2-4g02s).
func writeRegistryFile(t *testing.T, dir string, pid int, sessionID string) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/tmp","kind":"interactive"}`, pid, sessionID)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
	sidecar := fmt.Sprintf("%d.0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.key", pid)
	if err := os.WriteFile(filepath.Join(dir, sidecar), []byte("key-material"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

func upsertTestSessions(t *testing.T, ws *service.WriteService, ids ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range ids {
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
}

func runSweep(t *testing.T, ws *service.WriteService, dir string) {
	t.Helper()
	ctx := context.Background()
	sweeper := &GCSweeper{
		SessionsDir:     dir,
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
}

// alive reports whether the session row is visible (not soft-deleted).
func alive(t *testing.T, ss *sqlite.SessionStore, id string) bool {
	t.Helper()
	got, err := ss.GetByID(context.Background(), id, store.FreshnessWindow{Sessions: 24 * time.Hour})
	if err != nil {
		t.Fatalf("GetByID(%s): %v", id, err)
	}
	return got != nil
}

// TestGCSweeper_LiveRegistryFileKeepsRow: with the real <pid>.json layout, a
// session whose registry file is present MUST NOT be soft-deleted, and a
// session whose registry file is gone MUST be.
func TestGCSweeper_LiveRegistryFileKeepsRow(t *testing.T) {
	ws, ss := openTestWriteService(t)
	upsertTestSessions(t, ws, "sess-live", "sess-gone")

	dir := t.TempDir()
	writeRegistryFile(t, dir, 4242, "sess-live")

	runSweep(t, ws, dir)

	if !alive(t, ss, "sess-live") {
		t.Error("sess-live: registry file 4242.json present, want row alive, got soft-deleted")
	}
	if alive(t, ss, "sess-gone") {
		t.Error("sess-gone: no registry file, want soft-deleted, got alive")
	}
}

// TestGCSweeper_RevivesWhenRegistryFileReappears: a soft-deleted row whose
// registry file reappears MUST have deleted_at cleared.
func TestGCSweeper_RevivesWhenRegistryFileReappears(t *testing.T) {
	ws, ss := openTestWriteService(t)
	upsertTestSessions(t, ws, "sess-back")

	dir := t.TempDir()
	// An unrelated session keeps the dir non-empty; sess-back has no file yet.
	writeRegistryFile(t, dir, 1, "sess-other")
	runSweep(t, ws, dir)
	if alive(t, ss, "sess-back") {
		t.Fatal("precondition: sess-back should be soft-deleted while its registry file is absent")
	}

	writeRegistryFile(t, dir, 9001, "sess-back")
	runSweep(t, ws, dir)
	if !alive(t, ss, "sess-back") {
		t.Error("sess-back: registry file reappeared, want revived (deleted_at cleared), got soft-deleted")
	}
}

// TestGCSweeper_FilenameStemIsNotASessionID is the negative case for the old
// stem-as-id behaviour: a <pid>.json file must NOT keep alive a row whose
// session id equals the filename stem when the file's sessionId names a
// different session.
func TestGCSweeper_FilenameStemIsNotASessionID(t *testing.T) {
	ws, ss := openTestWriteService(t)
	upsertTestSessions(t, ws, "4242", "sess-real")

	dir := t.TempDir()
	writeRegistryFile(t, dir, 4242, "sess-real")

	runSweep(t, ws, dir)

	if !alive(t, ss, "sess-real") {
		t.Error("sess-real: want alive")
	}
	if alive(t, ss, "4242") {
		t.Error(`row "4242" (the pid): want soft-deleted, got alive; filenames must not be treated as session ids`)
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

// TestListSessionFiles_ReturnsSessionIDsNotPIDs verifies the keep set is
// derived from the sessionId field inside each <pid>.json, not the filename,
// and that sidecars, non-registry files, empty/malformed registry files and
// directories contribute nothing.
func TestListSessionFiles_ReturnsSessionIDsNotPIDs(t *testing.T) {
	dir := t.TempDir()
	writeRegistryFile(t, dir, 111, "sess-a")
	writeRegistryFile(t, dir, 222, "sess-b")
	for name, body := range map[string]string{
		"333.json":    `not json`,              // malformed: skipped like Discover
		"444.json":    `{"pid":444}`,           // no sessionId: nothing to keep
		"ignored.txt": `{"sessionId":"txt"}`,   // not a registry file
		"555.jsonl":   `{"sessionId":"jsonl"}`, // not a registry file
		"sub.json":    "",                      // replaced by a directory below
	} {
		if name == "sub.json" {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := listSessionFiles(dir)
	if err != nil {
		t.Fatalf("listSessionFiles: %v", err)
	}
	sort.Strings(ids)
	want := []string{"sess-a", "sess-b"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("listSessionFiles = %v, want %v", ids, want)
	}
}

// TestListSessionFiles_IgnoresStatusSiblings proves session-adjacent siblings
// (ADR 0021 §2; pg2-gzrn2; pg2-thkh0.5) never contribute ids, even when they
// are valid JSON carrying a sessionId. A genuine <pid>.json still yields its
// session id.
func TestListSessionFiles_IgnoresStatusSiblings(t *testing.T) {
	dir := t.TempDir()
	writeRegistryFile(t, dir, 777, "real-1")
	for _, name := range []string{
		"sess-1.status.jsonl",      // rate_limits sibling
		"sess-1.status.last",       // hash sidecar
		"sess-1.session-mode.json", // session-mode record
		"sess-1.compaction.json",   // compaction marker
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"sessionId":"phantom"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := listSessionFiles(dir)
	if err != nil {
		t.Fatalf("listSessionFiles: %v", err)
	}
	if fmt.Sprint(ids) != fmt.Sprint([]string{"real-1"}) {
		t.Errorf("listSessionFiles = %v, want [real-1]", ids)
	}
}
