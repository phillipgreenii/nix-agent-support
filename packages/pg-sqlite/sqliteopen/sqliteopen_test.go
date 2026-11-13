package sqliteopen

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestStandardWriterPragmas(t *testing.T) {
	db, err := Open(Standard(filepath.Join(t.TempDir(), "sub", "w.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := Effective(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	want := Pragmas{
		JournalMode: "wal", Synchronous: 1, BusyTimeout: DefaultBusyTimeoutMillis, CacheSize: -DefaultCacheKiB,
		MmapSize: DefaultMmapBytes, JournalSizeLimit: DefaultJournalSizeLimit, TempStore: 2, ForeignKeys: true,
	}
	if got != want {
		t.Fatalf("pragmas = %+v, want %+v", got, want)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatalf("writer pool = %d, want 1", db.Stats().MaxOpenConnections)
	}
}

func TestReadOnlyPoolPragmasOnEveryConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	w, err := Open(Standard(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if _, err := w.ExecContext(context.Background(), `CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(ReadOnly(path, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if ro.Stats().MaxOpenConnections != 3 {
		t.Fatalf("read pool = %d, want 3", ro.Stats().MaxOpenConnections)
	}
	// Hold three connections at once so the pool really opens three.
	ctx := context.Background()
	var held []*sql.Conn
	for i := 0; i < 3; i++ {
		c, err := ro.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
		var cache, qo, temp int
		var mmap int64
		for q, dst := range map[string]any{"PRAGMA cache_size": &cache, "PRAGMA query_only": &qo, "PRAGMA temp_store": &temp, "PRAGMA mmap_size": &mmap} {
			if err := c.QueryRowContext(ctx, q).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		if cache != -DefaultCacheKiB || qo != 1 || temp != 2 || mmap != DefaultMmapBytes {
			t.Fatalf("conn %d: cache=%d query_only=%d temp_store=%d mmap=%d", i, cache, qo, temp, mmap)
		}
	}
	for _, c := range held {
		_ = c.Close()
	}
	if _, err := ro.ExecContext(context.Background(), `INSERT INTO t VALUES (1)`); err == nil {
		t.Fatal("write through the read-only pool succeeded")
	}
	// A reader and the writer coexist: the writer commits while a read pool is open.
	if _, err := w.ExecContext(context.Background(), `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatalf("writer blocked by the read pool: %v", err)
	}
	var n int
	if err := ro.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read pool count = %d, %v", n, err)
	}
}

func TestReadOnlyOpenOfMissingFileCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(ReadOnly(filepath.Join(dir, "sub", "absent.db"), 0)); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Fatalf("read-only open created a directory (%v)", err)
	}
}

func TestOverrides(t *testing.T) {
	o := Standard(filepath.Join(t.TempDir(), "o.db"))
	o.Synchronous, o.TxLockImmediate, o.NoForeignKeys, o.MmapBytes = "FULL", true, true, -1
	db, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := Effective(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got.Synchronous != 2 || got.ForeignKeys || got.MmapSize != 0 {
		t.Fatalf("overrides not applied: %+v", got)
	}
}
