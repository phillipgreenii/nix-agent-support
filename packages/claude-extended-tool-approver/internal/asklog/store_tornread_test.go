package asklog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is the pg2-do92x regression test. It was filed as "evaluate
// --misses-only --format=json errors 'database disk image is malformed'
// while the equivalent full evaluate succeeds against the exact same
// database file". Reading the actual code (cmd_evaluate.go) ruled out the
// bead's leading hypothesis (a 2GiB/int32 boundary bug) immediately:
// --misses-only issues no SQL of its own — cmd_evaluate.go calls the exact
// same s.QueryRows(sinceDate) either way and only filters the returned slice
// in Go afterwards — so there was never a distinct "--misses-only query
// path" that a size-dependent bug could live in independently of plain
// evaluate.
//
// Direct reproduction against the real, live, actively-growing production
// corpus (~2.03GB, ~495k pages, many concurrent Claude Code sessions
// appending to it) confirmed this: `evaluate --misses-only --format=json`
// failed with "error querying rows: database disk image is malformed (11)"
// within ~7s on one attempt, and a subsequent `evaluate --misses-only
// --format=json` against the SAME file succeeded outright (ran the full
// ~72s replay to completion) — the failure is TIME-dependent (whether some
// other process's WAL checkpoint is mid-flight against the exact pages this
// scan is reading), not flag-dependent or size-dependent. This is exactly
// the torn-read hazard NewReadOnlyStore's own doc comment already predicted
// for ANY full-table scan through an immutable=1 connection racing a live
// checkpoint.
//
// A multi-GB fixture is not required to exercise that mechanism: the same
// SQLITE_CORRUPT (11) is deterministically reproducible in a small, fast
// unit test by corrupting one real on-disk page's cell-pointer array
// directly (see corruptFirstCellPointer below) — no timing race, no
// concurrent writer needed to PRODUCE the error, only to test the RETRY
// that now recovers from it.

// corruptFirstCellPointer flips the first entry of a leaf table b-tree
// page's cell-pointer array (the 2-byte big-endian offset, at byte 8 of the
// page, right after its 8-byte leaf header) to 0xFFFF — an offset far
// outside any 4096-byte page. Reading that page's first cell then walks off
// the page, which SQLite's own page-sanity checks reject as SQLITE_CORRUPT
// ("database disk image is malformed"), exactly the error pg2-do92x
// reported. It returns the 2 original bytes so the caller can restore them
// (simulating the torn state resolving, as a live checkpoint completing
// would).
func corruptFirstCellPointer(t *testing.T, dbPath string, pageSize, pgno int) (offset int64, original [2]byte) {
	t.Helper()
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open db file for corruption: %v", err)
	}
	defer func() { _ = f.Close() }()

	offset = int64(pgno-1)*int64(pageSize) + 8
	if _, err := f.ReadAt(original[:], offset); err != nil {
		t.Fatalf("read original cell pointer bytes: %v", err)
	}
	if _, err := f.WriteAt([]byte{0xFF, 0xFF}, offset); err != nil {
		t.Fatalf("corrupt cell pointer bytes: %v", err)
	}
	return offset, original
}

// corruptCellCount flips a leaf table b-tree page's cell-count field (the
// 2-byte big-endian count at byte 3 of the page, per the SQLite file format)
// to an implausibly large value. Unlike corruptFirstCellPointer (which only
// derails a scan that happens to dereference cell-pointer-array index 0),
// EVERY access to the page — a full scan and a direct ROWID seek alike —
// must read this field first to bound its search, so it reliably reproduces
// SQLITE_CORRUPT ("database disk image is malformed") for either access
// pattern. Confirmed empirically (tc-56z3r) against modernc.org/sqlite:
// corruptFirstCellPointer alone did not reproduce the error through a ROWID
// IN(...) lookup (SQLite's rowid seek does not always dereference pointer
// index 0), only through a full scan — this corruption reproduces it for
// both, which is what QueryRowsByIDs' regression test needs.
func corruptCellCount(t *testing.T, dbPath string, pageSize, pgno int) (offset int64, original [2]byte) {
	t.Helper()
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open db file for corruption: %v", err)
	}
	defer func() { _ = f.Close() }()

	offset = int64(pgno-1)*int64(pageSize) + 3
	if _, err := f.ReadAt(original[:], offset); err != nil {
		t.Fatalf("read original cell-count bytes: %v", err)
	}
	if _, err := f.WriteAt([]byte{0x7F, 0xFF}, offset); err != nil {
		t.Fatalf("corrupt cell-count bytes: %v", err)
	}
	return offset, original
}

func restoreBytes(t *testing.T, dbPath string, offset int64, original [2]byte) {
	t.Helper()
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open db file to restore: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt(original[:], offset); err != nil {
		t.Fatalf("restore original cell pointer bytes: %v", err)
	}
}

// seedRowsAndLocateLeafPage creates dbPath, writes n tool_decisions rows,
// fully checkpoints and closes the writer (so every row lives in the main
// file, not an unmerged WAL), and returns the page size and the page number
// of one of tool_decisions' own leaf pages (via the dbstat virtual table --
// modernc.org/sqlite is built with SQLITE_ENABLE_DBSTAT_VTAB).
func seedRowsAndLocateLeafPage(t *testing.T, dbPath string, n int) (pageSize, pgno int) {
	t.Helper()
	w, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for i := 0; i < n; i++ {
		if _, err := w.db.Exec(`INSERT INTO tool_decisions
			(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at)
			VALUES (?, '/tmp/pg2-do92x', 'Bash', ?, '{}', 'pending', '2026-01-01T00:00:00Z')`,
			fmt.Sprintf("s%d", i), fmt.Sprintf("h%d", i)); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	if err := w.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("PRAGMA page_size: %v", err)
	}
	if _, err := w.db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := w.db.QueryRow(
		"SELECT pageno FROM dbstat WHERE name='tool_decisions' AND pagetype='leaf' ORDER BY pageno LIMIT 1",
	).Scan(&pgno); err != nil {
		t.Fatalf("locate tool_decisions leaf page via dbstat: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close write store: %v", err)
	}
	return pageSize, pgno
}

// TestIsTornReadError_RecognizesSQLiteCorrupt is a narrow unit check on the
// classifier QueryRows' retry loop keys on, independent of the heavier
// file-corruption tests below.
func TestIsTornReadError_RecognizesSQLiteCorrupt(t *testing.T) {
	if isTornReadError(nil) {
		t.Error("nil error must not be treated as a torn read")
	}
	if isTornReadError(fmt.Errorf("no such table: tool_decisions")) {
		t.Error("an unrelated SQL error must not be treated as a torn read")
	}
	if !isTornReadError(fmt.Errorf("error querying rows: database disk image is malformed (11)")) {
		t.Error("the exact pg2-do92x error text must be recognized as a torn read")
	}
}

// TestQueryRows_RecoversFromTransientTornRead is the core pg2-do92x
// regression test: a full-table scan that hits SQLITE_CORRUPT on its first
// attempt (a corrupted cell pointer, standing in for a page caught mid
// checkpoint-write) succeeds once the underlying condition clears — exactly
// as it would once a live writer's WAL checkpoint finishes — because
// QueryRows retries through a brand-new connection rather than giving up or
// trusting a cached bad page.
func TestQueryRows_RecoversFromTransientTornRead(t *testing.T) {
	prevDelay := SetTornReadRetryDelayForTests(5 * time.Millisecond)
	defer SetTornReadRetryDelayForTests(prevDelay)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")
	const wantRows = 25

	pageSize, pgno := seedRowsAndLocateLeafPage(t, dbPath, wantRows)
	offset, original := corruptFirstCellPointer(t, dbPath, pageSize, pgno)

	// Sanity check the corruption actually reproduces the reported error
	// BEFORE testing recovery from it — otherwise a healed-too-early race
	// would make this test pass for the wrong reason. This does not mutate
	// the file (a read-only store, a plain SELECT), so the corrupted bytes
	// captured in `original` above are still the true pre-corruption value —
	// reading them again here (as corruptFirstCellPointer would on a second
	// call) would capture the CORRUPTED bytes as "original" instead.
	probe, err := NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore (probe): %v", err)
	}
	if _, err := probe.queryRowsOnce(""); err == nil || !isTornReadError(err) {
		_ = probe.Close()
		restoreBytes(t, dbPath, offset, original)
		t.Fatalf("corruption did not reproduce a torn-read error (queryRowsOnce err = %v); the test fixture needs adjusting, not the retry logic", err)
	}
	_ = probe.Close()

	// The file is still corrupted from the single corruptFirstCellPointer
	// call above (the probe only read it). Arrange for it to heal shortly
	// after QueryRows' first attempt fails, mirroring a live checkpoint
	// completing mid-retry.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		restoreBytes(t, dbPath, offset, original)
	}()

	r, err := NewReadOnlyStore(dbPath)
	if err != nil {
		wg.Wait()
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = r.Close() }()

	rows, err := r.QueryRows("")
	wg.Wait()
	if err != nil {
		t.Fatalf("QueryRows did not recover from a transient torn read: %v", err)
	}
	if len(rows) != wantRows {
		t.Errorf("QueryRows returned %d rows after recovering, want %d", len(rows), wantRows)
	}
}

// TestQueryRowsByIDs_RecoversFromTransientTornRead is TestQueryRows_
// RecoversFromTransientTornRead's counterpart for the `show` subcommand's
// query. Before tc-56z3r, QueryRowsByIDs had no torn-read retry of its own
// even though it opens the exact same immutable=1 connection as QueryRows
// and walks the exact same tool_decisions b-tree pages by rowid — this test
// pins that QueryRowsByIDs now shares QueryRows' recovery behavior via
// withTornReadRetry.
func TestQueryRowsByIDs_RecoversFromTransientTornRead(t *testing.T) {
	prevDelay := SetTornReadRetryDelayForTests(5 * time.Millisecond)
	defer SetTornReadRetryDelayForTests(prevDelay)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")
	const wantRows = 25

	pageSize, pgno := seedRowsAndLocateLeafPage(t, dbPath, wantRows)
	// corruptCellCount, not corruptFirstCellPointer: QueryRowsByIDs issues a
	// direct ROWID seek rather than a page scan, and empirically (tc-56z3r)
	// does not always dereference cell-pointer-array index 0 on the way to
	// its target row, so corruptFirstCellPointer's targeted corruption does
	// not reliably reproduce SQLITE_CORRUPT through this access pattern.
	offset, original := corruptCellCount(t, dbPath, pageSize, pgno)

	ids := make([]int, wantRows)
	for i := range ids {
		ids[i] = i + 1
	}

	probe, err := NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore (probe): %v", err)
	}
	if _, err := probe.queryRowsByIDsOnce(ids); err == nil || !isTornReadError(err) {
		_ = probe.Close()
		restoreBytes(t, dbPath, offset, original)
		t.Fatalf("corruption did not reproduce a torn-read error (queryRowsByIDsOnce err = %v); the test fixture needs adjusting, not the retry logic", err)
	}
	_ = probe.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		restoreBytes(t, dbPath, offset, original)
	}()

	r, err := NewReadOnlyStore(dbPath)
	if err != nil {
		wg.Wait()
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = r.Close() }()

	rows, err := r.QueryRowsByIDs(ids)
	wg.Wait()
	if err != nil {
		t.Fatalf("QueryRowsByIDs did not recover from a transient torn read: %v", err)
	}
	if len(rows) != wantRows {
		t.Errorf("QueryRowsByIDs returned %d rows after recovering, want %d", len(rows), wantRows)
	}
}

// TestQueryRows_PersistentCorruptionStillFails is the flip side: retrying
// must not paper over REAL, non-transient corruption. If the underlying
// condition never clears, QueryRows must exhaust its bounded retry budget
// and return a clear error — never hang, and never silently return
// truncated or wrong results.
func TestQueryRows_PersistentCorruptionStillFails(t *testing.T) {
	prevDelay := SetTornReadRetryDelayForTests(2 * time.Millisecond)
	defer SetTornReadRetryDelayForTests(prevDelay)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "asks.db")

	pageSize, pgno := seedRowsAndLocateLeafPage(t, dbPath, 25)
	corruptFirstCellPointer(t, dbPath, pageSize, pgno)
	// Deliberately never healed.

	r, err := NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("NewReadOnlyStore: %v", err)
	}
	defer func() { _ = r.Close() }()

	start := time.Now()
	rows, err := r.QueryRows("")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("QueryRows succeeded (%d rows) against a persistently corrupted page; it must surface an error, not silently return data", len(rows))
	}
	if !strings.Contains(strings.ToLower(err.Error()), "malformed") {
		t.Errorf("error = %q, want it to mention the disk-image-malformed error, not a swallowed/rewritten one", err)
	}
	// tornReadRetries=3 retries at delays 2ms,4ms,8ms is ~14ms of sleeping;
	// generous slack keeps this from flaking on a loaded machine while still
	// catching a regression to an unbounded/very slow retry loop.
	if elapsed > 5*time.Second {
		t.Errorf("QueryRows took %s to give up on persistent corruption; the retry budget must stay bounded", elapsed)
	}
}
