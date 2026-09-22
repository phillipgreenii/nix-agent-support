package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployRecordAllowsMissingFile(t *testing.T) {
	if deployRecordAllows(filepath.Join(t.TempDir(), "missing"), "abc") {
		t.Fatalf("expected false for a missing deploy record file")
	}
}

func TestDeployRecordAllowsEmptyPath(t *testing.T) {
	if deployRecordAllows("", "abc") {
		t.Fatalf("expected false when no deploy record path was configured")
	}
}

func TestDeployRecordAllowsMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploys.txt")
	content := "# deploy log\n\nabc123\ndef456\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !deployRecordAllows(path, "def456") {
		t.Fatalf("expected true for a hash present in the record")
	}
	if deployRecordAllows(path, "not-there") {
		t.Fatalf("expected false for a hash absent from the record")
	}
}
