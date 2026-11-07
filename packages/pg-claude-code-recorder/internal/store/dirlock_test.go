package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A second Open on a state directory a live Store holds fails fast and leaves
// index.db where it is; after Close the directory opens again.
func TestSecondOpenOnHeldStateDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	a := open(t, dir)
	id := seed(t, a)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrStateDirLocked) {
		t.Fatalf("second Open err = %v, want ErrStateDirLocked", err)
	}
	for _, n := range []string{DBName + ".corrupt", DBName + ".pre-rebuild"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Fatalf("%s was created by the refused Open", n)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := open(t, dir)
	defer func() { _ = b.Close() }()
	if _, err := b.File(id); err != nil {
		t.Fatal(err)
	}
}

// A busy database is a live database: Open must fail with the busy error and
// must NOT rename index.db aside as if it were corrupt. Regression for the
// two-daemon incident (see lockDir). Negative control: with the old rule (any
// openDB error sets the file aside) this test finds index.db.corrupt and the
// live index.db missing.
func TestBusyIndexIsNotSetAsideAsCorrupt(t *testing.T) {
	dir := t.TempDir()
	a := open(t, dir)
	seed(t, a)
	defer func() { _ = a.Close() }()

	// Hold the write lock the way a startup ParseAll does.
	tx, err := a.wdb.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('busy_probe','1')`); err != nil {
		t.Fatal(err)
	}

	old := busyTimeoutMS
	busyTimeoutMS = 50
	defer func() { busyTimeoutMS = old }()
	// Bypass the directory lock: this exercises the corrupt-vs-busy rule alone.
	b, err := openLocked(dir, Options{})
	if err == nil {
		_ = b.Close()
		t.Fatal("open against a write-locked index.db succeeded")
	}
	if isCorruptDBError(err) {
		t.Fatalf("busy error classified as corrupt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DBName+".corrupt")); err == nil {
		t.Fatal("live index.db was set aside as corrupt")
	}
	if _, err := os.Stat(filepath.Join(dir, DBName)); err != nil {
		t.Fatalf("live index.db is gone: %v", err)
	}
}
