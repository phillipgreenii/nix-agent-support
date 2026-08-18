package asklog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewReadOnlyStore_MissingFile_Errors is the "no MkdirAll, no create"
// half of the read-only contract: unlike NewStore, a missing database (or a
// missing parent directory) is a plain error, not a fresh empty database.
func TestNewReadOnlyStore_MissingFile_Errors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "does-not-exist", "asks.db")

	s, err := NewReadOnlyStore(dbPath)
	if err == nil {
		_ = s.Close()
		t.Fatal("NewReadOnlyStore on a missing database returned no error")
	}

	if _, statErr := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(statErr) {
		t.Error("NewReadOnlyStore created the parent directory; it must never write anything at open time")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Error("NewReadOnlyStore created the database file; it must never write anything at open time")
	}
}

// TestNewReadOnlyStore_ReadsExistingRows is the positive half: a database
// written by the real (read-write) store is fully readable through the
// read-only one.
func TestNewReadOnlyStore_ReadsExistingRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")

	w, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := w.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at)
		VALUES ('s', '/tmp', 'Bash', 'h1', '{}', 'pending', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close write store: %v", err)
	}

	r, err := NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = r.Close() }()

	rows, err := r.QueryRows("")
	if err != nil {
		t.Fatalf("QueryRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("QueryRows returned %d rows, want 1", len(rows))
	}
}

// dbFileStamp is the (size, mtime) pair used to prove a file was untouched.
// mtime alone is not quite enough on filesystems with coarse timestamp
// resolution; pairing it with size catches a same-second truncate-and-rewrite
// that mtime resolution could otherwise hide.
type dbFileStamp struct {
	exists bool
	size   int64
	mtime  int64
}

func statStamp(t *testing.T, path string) dbFileStamp {
	t.Helper()
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return dbFileStamp{}
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return dbFileStamp{exists: true, size: fi.Size(), mtime: fi.ModTime().UnixNano()}
}

// TestNewReadOnlyStore_WriteFailsAndLeavesFileAndSidecarsUntouched is the
// acceptance-criteria proof for pg2-cbihz: opening through
// NewReadOnlyStore, reading, and then attempting to WRITE (a) fails loudly
// instead of silently succeeding, and (b) leaves the main database file's
// (size, mtime) — and its WAL/SHM sidecars, present or not — completely
// unchanged.
//
// The write store is kept open (not Closed) while the read-only store does
// its work, specifically so the WAL/SHM sidecars are present on disk for
// this test to prove untouched — closing the sole connection to a WAL-mode
// SQLite database triggers an automatic checkpoint that merges and deletes
// them, which would make "sidecars unchanged" trivially true by having no
// sidecars to change.
func TestNewReadOnlyStore_WriteFailsAndLeavesFileAndSidecarsUntouched(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")

	w, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = w.Close() }()

	if _, err := w.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at)
		VALUES ('s', '/tmp', 'Bash', 'h1', '{}', 'pending', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	// Checkpoint so the schema and the seeded row are in the MAIN file, not
	// only the WAL: immutable=1 (see NewReadOnlyStore's doc comment) reads
	// only the main file and never looks at an unmerged WAL, so without this
	// a fresh WAL-mode database (schema + row both still WAL-only, as they
	// are for any database that has never checkpointed) would look like an
	// empty database to the read-only store -- exactly the caveat documented
	// there, just total instead of partial because nothing has ever
	// checkpointed yet. PASSIVE merges what it can without blocking and,
	// with only one connection attached and no concurrent writer, merges
	// everything; the -wal/-shm files stay present (just idle) because `w`
	// remains open, which is what this test needs them to do.
	if _, err := w.db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"

	before := map[string]dbFileStamp{
		dbPath:  statStamp(t, dbPath),
		walPath: statStamp(t, walPath),
		shmPath: statStamp(t, shmPath),
	}

	r, err := NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore: %v", err)
	}

	// Reading must still work.
	var count int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM tool_decisions").Scan(&count); err != nil {
		t.Fatalf("read through NewReadOnlyStore: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	// Writing must fail loudly, not succeed silently.
	_, writeErr := r.db.Exec("UPDATE tool_decisions SET excluded = 1 WHERE id = 1")
	if writeErr == nil {
		t.Fatal("write through NewReadOnlyStore succeeded; it must be rejected")
	}
	if !strings.Contains(strings.ToLower(writeErr.Error()), "readonly") {
		t.Errorf("write error = %q, want it to mention a readonly database", writeErr)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("close read-only store: %v", err)
	}

	after := map[string]dbFileStamp{
		dbPath:  statStamp(t, dbPath),
		walPath: statStamp(t, walPath),
		shmPath: statStamp(t, shmPath),
	}

	for path, wantStamp := range before {
		gotStamp := after[path]
		if gotStamp != wantStamp {
			t.Errorf("%s changed across the read-only open/read/failed-write/close cycle: before=%+v after=%+v", path, wantStamp, gotStamp)
		}
	}

	// The write store never saw its own row get excluded, confirming the
	// UPDATE truly never landed (not merely that it errored on a
	// disconnected view).
	var excluded int
	if err := w.db.QueryRow("SELECT excluded FROM tool_decisions WHERE id = 1").Scan(&excluded); err != nil {
		t.Fatalf("re-read via write store: %v", err)
	}
	if excluded != 0 {
		t.Errorf("excluded = %d, want 0 (the rejected UPDATE must not have applied)", excluded)
	}
}

// TestNewReadOnlyStore_DoesNotMigrate proves the read-only path never
// upgrades the schema: querying a table introduced by a migration that was
// never run reports a normal SQL error, not a table conjured into
// existence by an implicit migrate() call.
func TestNewReadOnlyStore_DoesNotMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "empty.db")

	// A zero-byte file is a valid (empty) SQLite database as far as
	// sqlite3_open_v2 is concerned, but it has no tables at all -- a stand-in
	// for "a file exists but this binary must not use a read to upgrade it".
	if err := os.WriteFile(dbPath, nil, 0o644); err != nil {
		t.Fatalf("create empty file: %v", err)
	}
	before := statStamp(t, dbPath)

	r, err := NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = r.Close() }()

	_, err = r.db.Exec("SELECT 1 FROM tool_decisions")
	if err == nil {
		t.Fatal("querying tool_decisions on an unmigrated file succeeded; NewReadOnlyStore must not have migrated it")
	}
	if !strings.Contains(err.Error(), "no such table") {
		t.Errorf("error = %q, want \"no such table\"", err)
	}

	after := statStamp(t, dbPath)
	if after != before {
		t.Errorf("empty database file changed across NewReadOnlyStore + a failed query: before=%+v after=%+v", before, after)
	}
}
