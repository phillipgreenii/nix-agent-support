package store

import (
	"os"
	"path/filepath"
	"testing"
)

// OpenReadOnly must read an existing store, refuse every write, and never
// create a missing file (INV-LINKS-1: the links verb is read-only).
func TestOpenReadOnlyReadsButNeverWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Facts: "{}", AsOf: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()

	e, found, err := ro.GetEntity("o/r", "pr", "o/r#1")
	if err != nil || !found || e.EntityID != "o/r#1" {
		t.Fatalf("GetEntity = %+v, %v, %v", e, found, err)
	}
	if v, err := ro.SchemaVersion(); err != nil || v != 2 {
		t.Fatalf("SchemaVersion = %d, %v; want 2", v, err)
	}
	if err := ro.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#2", Facts: "{}", AsOf: "x"}); err == nil {
		t.Fatal("write through a read-only handle succeeded")
	}
}

func TestOpenReadOnlyMissingFileIsAnErrorAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "absent.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("OpenReadOnly on a missing store returned no error")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnly created %s (stat err: %v)", filepath.Join(dir, "sub"), err)
	}
}

// An empty (schema-less) file is not a readable pg-desk store.
func TestOpenReadOnlyEmptyFileFailsOnFirstRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err == nil {
		defer func() { _ = ro.Close() }()
		if _, _, err = ro.GetEntity("o/r", "pr", "x"); err == nil {
			t.Fatal("reading a schema-less file returned no error")
		}
	}
}

// The writer and every connection of the read-only pool carry the standard
// pragma set (the shared helper's, proven field by field in its own tests); this
// proves pg-desk's opens apply it, on real SQLite, and that a read pass on the
// pool is not blocked by an open writer transaction.
func TestWriterAndReadPoolCarryTheStandardPragmas(t *testing.T) {
	// Other tests switch fsync off for speed; this one needs the production value.
	prev := synchronousPragma
	SetSynchronousForTests("")
	t.Cleanup(func() { SetSynchronousForTests(prev) })
	w, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.SeedLiveSizeForTest(t, 20, 100, 5)
	rd, err := OpenReadPool(w.Path(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()

	check := func(name string, s *Store, wantQueryOnly bool, wantJournal string) {
		t.Helper()
		var cache, temp, qo int
		var mmap int64
		var journal string
		for q, dst := range map[string]any{
			"PRAGMA cache_size": &cache, "PRAGMA temp_store": &temp, "PRAGMA mmap_size": &mmap,
			"PRAGMA query_only": &qo, "PRAGMA journal_mode": &journal,
		} {
			if err := s.sql.QueryRow(q).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		if cache != -65536 || temp != 2 || mmap != 256<<20 || (qo == 1) != wantQueryOnly || journal != wantJournal {
			t.Fatalf("%s: cache=%d temp_store=%d mmap=%d query_only=%d journal=%s", name, cache, temp, mmap, qo, journal)
		}
	}
	check("writer", w, false, "wal")
	check("read pool", rd, true, "wal")
	var sync, fk int
	if err := w.sql.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil || sync != 1 {
		t.Fatalf("writer synchronous = %d, %v; want NORMAL (1)", sync, err)
	}
	if err := w.sql.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("writer foreign_keys = %d, %v", fk, err)
	}
	if rd.sql.Stats().MaxOpenConnections != 3 {
		t.Fatalf("read pool size = %d", rd.sql.Stats().MaxOpenConnections)
	}
	if err := rd.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: "x", Facts: "{}", AsOf: "x"}); err == nil {
		t.Fatal("write through the read pool succeeded")
	}
	// An open write transaction on the writer does not block a read on the pool.
	tx, err := w.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE entity SET stale = 1`); err != nil {
		t.Fatal(err)
	}
	if n, err := rd.CountEntities(); err != nil || n != 20 {
		t.Fatalf("read pool during a writer transaction: %d, %v", n, err)
	}
}
