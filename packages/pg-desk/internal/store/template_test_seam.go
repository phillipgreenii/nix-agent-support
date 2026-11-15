package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// newStoreTemplate holds the bytes of a freshly created, closed schema-2
// store, set by EnableNewStoreTemplateForTests. Production never sets it.
var newStoreTemplate []byte

// EnableNewStoreTemplateForTests makes Open create a NEW store file by copying
// a template built once, here, through the real Open (so the template is
// exactly what Open produces on a fresh path), instead of re-running the
// schema DDL on every call. Under -race those DDL statements dominate a test
// package that opens hundreds of stores. Open of an existing file is
// unchanged, so migration and refusal paths are still exercised by the tests
// that open a pre-built or old-schema file. Call it once from TestMain, after
// SetSynchronousForTests; it returns an error if the template cannot be built.
func EnableNewStoreTemplateForTests() error {
	newStoreTemplate = nil
	dir, err := os.MkdirTemp("", "pg-desk-store-template-")
	if err != nil {
		return fmt.Errorf("store template: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "template.db")
	s, err := Open(path)
	if err != nil {
		return fmt.Errorf("store template: %w", err)
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("store template: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("store template: %w", err)
	}
	newStoreTemplate = b
	return nil
}

// openFromTemplate copies the template to path when it is enabled and path
// does not exist yet, and reports whether it did.
func openFromTemplate(path string) (bool, error) {
	if newStoreTemplate == nil {
		return false, nil
	}
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("store: create dir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, newStoreTemplate, 0o644); err != nil {
		return false, fmt.Errorf("store: copy template to %s: %w", path, err)
	}
	return true, nil
}
