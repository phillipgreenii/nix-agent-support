package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	want := snapshot{ZombieCount: 7, ZombieConsecutiveGrowth: 2, CheckedAt: "2026-09-22T00:00:00Z"}
	if err := saveSnapshot(path, want); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}
	got, ok := loadSnapshot(path)
	if !ok {
		t.Fatalf("expected loadSnapshot to succeed")
	}
	if got.ZombieCount != want.ZombieCount || got.ZombieConsecutiveGrowth != want.ZombieConsecutiveGrowth {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got.Version != snapshotVersion {
		t.Fatalf("version = %d, want %d", got.Version, snapshotVersion)
	}
}

// TestLoadSnapshotMissingFileIsNoBaseline covers the "missing" third of
// [design: "Snapshot robustness"]'s missing/corrupted/version-mismatched
// trio.
func TestLoadSnapshotMissingFileIsNoBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	_, ok := loadSnapshot(path)
	if ok {
		t.Fatalf("expected ok=false for a missing snapshot file")
	}
}

// TestLoadSnapshotCorruptFileIsNoBaseline covers the "corrupted" third.
func TestLoadSnapshotCorruptFileIsNoBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok := loadSnapshot(path)
	if ok {
		t.Fatalf("expected ok=false for a corrupted snapshot file")
	}
}

// TestLoadSnapshotVersionMismatchIsNoBaseline covers the
// "version-mismatched" third.
func TestLoadSnapshotVersionMismatchIsNoBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-version.json")
	if err := os.WriteFile(path, []byte(`{"version":9999,"zombie_count":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok := loadSnapshot(path)
	if ok {
		t.Fatalf("expected ok=false for a version-mismatched snapshot file")
	}
}

// TestSaveSnapshotAlwaysStampsCurrentVersion proves a caller-supplied
// (or zero) Version field is always overwritten with snapshotVersion on
// save, so a stale/uninitialized value can never leak onto disk.
func TestSaveSnapshotAlwaysStampsCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := saveSnapshot(path, snapshot{Version: 42, ZombieCount: 1}); err != nil {
		t.Fatal(err)
	}
	got, ok := loadSnapshot(path)
	if !ok || got.Version != snapshotVersion {
		t.Fatalf("got version %d ok=%v, want %d/true", got.Version, ok, snapshotVersion)
	}
}

// TestSaveSnapshotCreatesMissingParentDirectories is the pg2-d845f
// regression: the default location ($HOME/.local/state/ccpool-probe) does
// not exist on a fresh host, and saveSnapshot used to fail there forever.
func TestSaveSnapshotCreatesMissingParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "ccpool-probe", "snapshot.json")
	if err := saveSnapshot(path, snapshot{ZombieCount: 3}); err != nil {
		t.Fatalf("saveSnapshot with missing parents: %v", err)
	}
	got, ok := loadSnapshot(path)
	if !ok || got.ZombieCount != 3 {
		t.Fatalf("round trip after mkdir: got %+v ok=%v", got, ok)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("created directory mode = %o, want 700", perm)
	}
}

// TestSaveSnapshotParentIsFileFails: the error is returned, not swallowed.
func TestSaveSnapshotParentIsFileFails(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveSnapshot(filepath.Join(blocker, "snapshot.json"), snapshot{}); err == nil {
		t.Fatalf("expected an error when the parent path is a regular file")
	}
}
