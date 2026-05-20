package treestate

import (
	"os"
	"testing"
)

func TestLoadEmptyOnMissingFile(t *testing.T) {
	s := Load(t.TempDir())
	if s.IsCollapsed("/any") {
		t.Error("new state should have no collapsed paths")
	}
}

func TestLoadEmptyOnEmptyCacheDir(t *testing.T) {
	s := Load("")
	if s.IsCollapsed("/any") {
		t.Error("empty cacheDir should produce empty state")
	}
}

func TestToggleAndIsCollapsed(t *testing.T) {
	s := Load(t.TempDir())
	s.Toggle("/a")
	if !s.IsCollapsed("/a") {
		t.Error("path should be collapsed after Toggle")
	}
	s.Toggle("/a")
	if s.IsCollapsed("/a") {
		t.Error("path should be expanded after second Toggle")
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s := Load(dir)
	s.Toggle("/x")
	s.Toggle("/y")
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2 := Load(dir)
	if !s2.IsCollapsed("/x") {
		t.Error("expected /x collapsed after reload")
	}
	if !s2.IsCollapsed("/y") {
		t.Error("expected /y collapsed after reload")
	}
}

func TestLoadInvalidJSONReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/tree-state.json", []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Load(dir)
	if s.IsCollapsed("/any") {
		t.Error("invalid JSON should produce empty state")
	}
}

func TestSaveCreatesDir(t *testing.T) {
	dir := t.TempDir() + "/nested/dir"
	s := Load(dir)
	s.Toggle("/p")
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save should create directories: %v", err)
	}
	s2 := Load(dir)
	if !s2.IsCollapsed("/p") {
		t.Error("expected /p collapsed after save into new dir")
	}
}

func TestSaveEmptyCacheDirNoOp(t *testing.T) {
	s := Load("")
	s.Toggle("/p")
	if err := s.Save(""); err != nil {
		t.Errorf("Save with empty cacheDir should be no-op, got: %v", err)
	}
}
