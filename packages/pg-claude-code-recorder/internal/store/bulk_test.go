package store

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
)

// seededDir returns a state directory whose journal holds one file.
func seededDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s := open(t, dir)
	seed(t, s)
	_ = s.Close()
	return dir
}

func syncMode(t *testing.T, s *Store) int {
	t.Helper()
	conn, err := s.wdb.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var v int
	if err := conn.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUnfinishedBulkRebuildIsRefusedAndRebuildRecovers(t *testing.T) {
	dir := seededDir(t)
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncMode(t, s); got != 0 {
		t.Fatalf("synchronous during a bulk rebuild is %d, want 0 (OFF)", got)
	}
	_ = s.Close() // abandoned before FinishRebuild

	var unfinished *UnfinishedRebuildError
	if _, err := Open(dir, Options{}); !errors.As(err, &unfinished) {
		t.Fatalf("a normal open of an unfinished rebuild: %v, want UnfinishedRebuildError", err)
	}
	if _, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true}); err != nil {
		t.Fatalf("a rerun rebuild must replace the half file: %v", err)
	}
}

func TestFinishBulkRebuildClearsTheMarkerAndRestoresTheRunningSetting(t *testing.T) {
	dir := seededDir(t)
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.metaString(metaRebuildUnfinished) != "1" {
		t.Fatal("the marker is not written before the rebuild starts")
	}
	if err := s.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if got := syncMode(t, s); got != 1 {
		t.Fatalf("synchronous after finishing is %d, want 1 (NORMAL, the running setting; FULL only for the final checkpoint)", got)
	}
	if s.metaString(metaRebuildUnfinished) != "" {
		t.Fatal("the marker survived FinishRebuild")
	}
	_ = s.Close()
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("a finished rebuild must open normally: %v", err)
	}
	_ = s2.Close()
}

func TestBulkBatchCommitsOnBudgetAndRollsBackOnlyAFailedWrite(t *testing.T) {
	dir := seededDir(t)
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true, BulkRecords: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.BulkBegin()
	ins := func(v string) func(tx *sql.Tx) error {
		return func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "k"+v, v)
			return err
		}
	}
	if err := s.WithWrite("", ins("a")); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := s.WithWrite("", func(tx *sql.Tx) error {
		if err := ins("b")(tx); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := s.BulkAdd(5, 0); err != nil || s.BulkCommits() != 0 {
		t.Fatalf("under budget must not commit: %v commits=%d", err, s.BulkCommits())
	}
	if err := s.BulkAdd(5, 0); err != nil || s.BulkCommits() != 1 {
		t.Fatalf("at budget must commit once: %v commits=%d", err, s.BulkCommits())
	}
	if s.metaString("ka") != "a" || s.metaString("kb") != "" {
		t.Fatalf("committed ka=%q kb=%q, want a and nothing", s.metaString("ka"), s.metaString("kb"))
	}
	if err := s.BulkEnd(); err != nil {
		t.Fatal(err)
	}
}

// TestCrashMidBatchLeavesAFileTheNextStartRefuses crashes (an armed panic, as
// the other crash-point tests do) while a bulk rebuild holds an uncommitted
// batch; the real SIGKILL variant is in bulk_kill_test.go (integration tier).
func TestCrashMidBatchLeavesAFileTheNextStartRefuses(t *testing.T) {
	dir := seededDir(t)
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true, BulkRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	s.BulkBegin()
	_ = s.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('half','1')`)
		return err
	})
	disarm := faults.ArmPanic("rebuild.before-batch-commit")
	crashed := faults.Recover(func() { _ = s.BulkAdd(1, 0) })
	disarm()
	if !crashed {
		t.Fatal("the batch commit crash point was not reached")
	}
	var unfinished *UnfinishedRebuildError
	if _, err := Open(dir, Options{}); !errors.As(err, &unfinished) {
		t.Fatalf("next start after a crash mid-rebuild: %v, want UnfinishedRebuildError", err)
	}
}

// A deferred build of the unique index must fail loudly on duplicate records,
// name the rows, and leave the file marked unfinished (never drop a row).
func TestDeferredUniqueIndexViolationFailsTheRebuildAndReportsTheRows(t *testing.T) {
	dir := seededDir(t)
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ins := `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version) VALUES(7,1,100,0,5,'src','t',1)`
	for i := 0; i < 2; i++ {
		if _, err := s.wdb.Exec(ins); err != nil {
			t.Fatalf("a deferred load must accept the duplicate inline: %v", err)
		}
	}
	err = s.FinishRebuild()
	var dup *DuplicateRecordsError
	if !errors.As(err, &dup) {
		t.Fatalf("FinishRebuild: %v, want DuplicateRecordsError", err)
	}
	if len(dup.Rows) != 1 || dup.Rows[0] != (DuplicateRecord{File: 7, Gen: 1, Off: 100, Item: 0, Count: 2}) {
		t.Fatalf("reported rows %+v", dup.Rows)
	}
	var n int
	if err := s.wdb.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after the failure: %d %v, want both kept", n, err)
	}
	if s.metaString(metaRebuildUnfinished) != "1" {
		t.Fatal("the unfinished marker must stay after a failed index build")
	}
}
