package store

import "testing"

func hasTable(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestNewLayoutBulkRebuildHasNoTrigramTable(t *testing.T) {
	s, err := Open(t.TempDir(), Options{FreeBytes: ampleFreeBytes, NoTrigramIndex: true, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if s.HasTrigram() || hasTable(t, s, "fts_tri") || !hasTable(t, s, "fts_word") {
		t.Fatalf("layout: trigram %v", s.HasTrigram())
	}
}

func TestDefaultLayoutKeepsTrigramTable(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !s.HasTrigram() || !hasTable(t, s, "fts_tri") {
		t.Fatal("the running layout lost fts_tri")
	}
}
