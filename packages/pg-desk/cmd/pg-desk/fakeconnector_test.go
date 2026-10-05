package main

import (
	"os"
	"path/filepath"
	"testing"
)

// installFakePGConnector writes an executable named pg-connector, whose body
// is script (a shell script, shebang added), into t.TempDir() and prepends
// that directory to PATH for the test. Later cmd/pg-desk tests (changes,
// refresh, show) share it instead of each inventing their own.
func installFakePGConnector(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-connector"), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake pg-connector: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
