package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func corruptAsides(t *testing.T, path string) []string {
	t.Helper()
	m, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLoadLedgerQuarantinesUndecodableFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := LedgerKey{Type: "pr", Backend: "gh", Query: "q"}
	path, err := ledgerPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"entries": {"a": `), 0o644); err != nil {
		t.Fatal(err)
	}

	for call := 1; call <= 2; call++ {
		l, err := loadLedger(key)
		if err != nil {
			t.Fatalf("call %d: loadLedger error: %v", call, err)
		}
		if len(l.Entries) != 0 || l.Entries == nil || l.Consumers == nil {
			t.Fatalf("call %d: want empty non-nil ledger, got %+v", call, l)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("undecodable file still at its original path")
	}
	if got := corruptAsides(t, path); len(got) != 1 {
		t.Fatalf("want exactly one quarantined file, got %v", got)
	}
}

func TestLoadCacheQuarantinesUndecodableFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := CacheKey{Type: "pr", Backend: "gh"}
	path, err := cachePath(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"entries`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCache(key)
	if err != nil {
		t.Fatalf("loadCache error: %v", err)
	}
	if c.Entries == nil || len(c.Entries) != 0 {
		t.Fatalf("want empty cache, got %+v", c)
	}
	if got := corruptAsides(t, path); len(got) != 1 {
		t.Fatalf("want one quarantined file, got %v", got)
	}
}

func TestWriteFileAtomicRemovesOnlyStaleTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".ledger-old.tmp")
	fresh := filepath.Join(dir, ".ledger-new.tmp")
	other := filepath.Join(dir, ".cache-old.tmp")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTempAge)
	for _, p := range []string{stale, other} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFileAtomic(filepath.Join(dir, "l.json"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale ledger temp file was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file was removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a temp file of the other writer was removed: %v", err)
	}
}

func TestWriteFileAtomicSyncsBeforeRenameAndDirAfter(t *testing.T) {
	var steps []string
	orig := atomicFS
	t.Cleanup(func() { atomicFS = orig })
	atomicFS.Sync = func(f *os.File) error {
		steps = append(steps, "sync")
		return orig.Sync(f)
	}
	atomicFS.Rename = func(a, b string) error {
		steps = append(steps, "rename")
		return orig.Rename(a, b)
	}
	atomicFS.SyncDir = func(d string) error {
		steps = append(steps, "syncdir")
		return orig.SyncDir(d)
	}
	dir := t.TempDir()
	if err := writeFileAtomic(filepath.Join(dir, "l.json"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(steps, ","); got != "sync,rename,syncdir" {
		t.Fatalf("durability order = %s, want sync,rename,syncdir", got)
	}
}
