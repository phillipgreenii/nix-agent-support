package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A store created from the template is the store Open creates: same schema
// version, same tables and columns, same rows, writable.
func TestNewStoreTemplateMatchesRealOpen(t *testing.T) {
	ctx := context.Background()
	real := OpenForTest(t)
	realCols, err := real.tableColumns(ctx, "entity")
	if err != nil {
		t.Fatal(err)
	}

	if err := EnableNewStoreTemplateForTests(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { newStoreTemplate = nil })
	path := filepath.Join(t.TempDir(), "nested", "copy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if v, err := s.SchemaVersion(ctx); err != nil || v != NewSchemaVersion {
		t.Fatalf("schema version = %d (err %v), want %d", v, err, NewSchemaVersion)
	}
	gotCols, err := s.tableColumns(ctx, "entity")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCols) != len(realCols) {
		t.Fatalf("entity columns: got %d, real Open has %d", len(gotCols), len(realCols))
	}
	for c := range realCols {
		if !gotCols[c] {
			t.Errorf("entity column %q missing from the template store", c)
		}
	}
	if err := s.UpsertEntityForTest(ctx, Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Facts: `{}`, AsOf: "2026-09-16T00:00:00Z"}); err != nil {
		t.Fatalf("write to template store: %v", err)
	}
	// A second Open of the now-existing file takes the normal path and keeps the row.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if n, err := again.CountEntities(ctx); err != nil || n != 1 {
		t.Fatalf("entities after reopen = %d (err %v), want 1", n, err)
	}
}
