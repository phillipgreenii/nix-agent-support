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
	if v, err := ro.SchemaVersion(); err != nil || v != 1 {
		t.Fatalf("SchemaVersion = %d, %v; want 1", v, err)
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
