package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSnapshotMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	_, ok := loadSnapshot(path)
	if ok {
		t.Fatalf("expected ok=false for a missing snapshot file")
	}
}

func TestLoadSnapshotCorrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok := loadSnapshot(path)
	if ok {
		t.Fatalf("expected ok=false for a corrupted snapshot file")
	}
}

func TestLoadSnapshotVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"queue_depth":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok := loadSnapshot(path)
	if ok {
		t.Fatalf("expected ok=false for a version-mismatched snapshot file")
	}
}

func TestSaveAndLoadSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "snapshot.json")
	// saveSnapshot's own os.WriteFile does not create parent dirs -- this
	// test proves that constraint too, by pre-creating the dir, since
	// run.go's own caller is responsible for mkdir -p semantics, not this
	// file.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	want := snapshot{Version: snapshotVersion, QueueDepth: 7, Backlog: 3, BinaryHash: "abc123", CheckedAt: "2026-09-22T00:00:00Z"}
	if err := saveSnapshot(path, want); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}
	got, ok := loadSnapshot(path)
	if !ok {
		t.Fatalf("expected ok=true after a successful save")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSaveSnapshotStampsCurrentVersionRegardlessOfInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := saveSnapshot(path, snapshot{Version: 42}); err != nil {
		t.Fatal(err)
	}
	got, ok := loadSnapshot(path)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got.Version != snapshotVersion {
		t.Fatalf("got version %d, want %d", got.Version, snapshotVersion)
	}
}
