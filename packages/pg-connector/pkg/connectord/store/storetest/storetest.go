// Package storetest opens the store for a test without paying for the whole
// migration ladder each time.
//
// Climbing the ladder on an empty file is most of the cost of a test that only
// needs "a fresh, current store" (about 50 ms of CPU under -race, against a
// test body that takes a few milliseconds). Open climbs it once per test
// binary into a template, keeps the template's bytes in memory, and writes a
// copy into each test's own directory; store.Open then finds the file already
// at the latest version and only opens it.
//
// A test that is about the open itself (the migration outcome, a moved-aside
// store, the identity of a new store) MUST call store.Open on an empty path
// instead: every copy shares the template's store identity.
package storetest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// template returns the bytes of a store at the latest schema version, built
// once per test binary.
func template() ([]byte, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "storetest-template-")
		if err != nil {
			templateErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "store.db")
		s, _, err := store.Open(path, store.Options{Synchronous: "OFF"})
		if err != nil {
			templateErr = err
			return
		}
		// Close checkpoints the WAL into the main file, so the file alone is
		// the whole store.
		if err = s.Close(); err != nil {
			templateErr = err
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	return templateBytes, templateErr
}

// Open opens a fresh store at the latest schema version under t.TempDir with
// Synchronous "OFF" and closes it when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	b, err := template()
	if err != nil {
		t.Fatalf("storetest: build the template store: %v", err)
	}
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := store.Open(path, store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
