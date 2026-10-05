package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if !reflect.DeepEqual(got, want) {
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

// The default snapshot dir ($HOME/.local/state/pg-router-probe) does not exist
// on a fresh host; saveSnapshot must create it (pg2-3gqtw).
func TestSaveSnapshotCreatesMissingParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "pg-router-probe", "snapshot.json")
	want := snapshot{Version: snapshotVersion, Backlog: 70, CheckedAt: "2026-10-02T00:00:00Z"}
	if err := saveSnapshot(path, want); err != nil {
		t.Fatalf("saveSnapshot into a missing dir: %v", err)
	}
	got, ok := loadSnapshot(path)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v ok=%v, want %+v", got, ok, want)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("created dir has mode %o, want 700", perm)
	}
}

// A parent that cannot be created (a regular file in the way) still errors.
func TestSaveSnapshotUncreatableParentErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveSnapshot(filepath.Join(blocker, "snapshot.json"), snapshot{}); err == nil {
		t.Fatalf("expected an error when the parent path is a regular file")
	}
}

// pg2-1jkai: binary_path is additive. A snapshot written before the field
// existed still loads at the current version (no baseline reset) with an
// empty = unknown path, and the field round-trips once set.
func TestLoadSnapshotWithoutBinaryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	raw := `{"version":1,"queue_depth":1,"backlog":2,"binary_hash":"abc","checked_at":"2026-09-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := loadSnapshot(path)
	if !ok || got.BinaryHash != "abc" || got.BinaryPath != "" {
		t.Fatalf("got %+v ok=%v, want hash abc and empty path", got, ok)
	}
}

func TestSaveAndLoadSnapshotRoundTripsBinaryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	want := snapshot{Version: snapshotVersion, BinaryHash: "abc", BinaryPath: "/nix/store/x-y/bin/z"}
	if err := saveSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadSnapshot(path)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v ok=%v, want %+v", got, ok, want)
	}
}

// pg2-3tt2e: alerts is additive and omitempty. A pre-existing snapshot
// without it still loads at the current version (no baseline reset), a
// snapshot with no alert state serializes without the key, and the state
// round-trips once set.
func TestSnapshotAlertsFieldIsAdditive(t *testing.T) {
	if snapshotVersion != 1 {
		t.Fatalf("snapshotVersion = %d: adding alerts must not bump it", snapshotVersion)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	raw := `{"version":1,"queue_depth":1,"backlog":2,"binary_hash":"abc","checked_at":"2026-09-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := loadSnapshot(path)
	if !ok || got.Backlog != 2 || got.Alerts != nil {
		t.Fatalf("got %+v ok=%v", got, ok)
	}

	if err := saveSnapshot(path, snapshot{Backlog: 2}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "alerts") {
		t.Fatalf("empty alert state must be omitted, got %s", data)
	}

	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	want := snapshot{Version: snapshotVersion, Alerts: map[string]alertState{
		"fp": {LastStartsAt: start, Episodes: []time.Time{start}, LastNoted: start.Add(time.Hour)},
	}}
	if err := saveSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok = loadSnapshot(path)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v ok=%v, want %+v", got, ok, want)
	}
}
