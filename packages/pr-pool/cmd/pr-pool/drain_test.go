package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSelfLogin(t *testing.T) {
	got, err := parseSelfLogin([]byte(`{"self_login":"phillipg","worktree_root":"/x"}`))
	if err != nil || got != "phillipg" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestParseSelfLogin_empty(t *testing.T) {
	if _, err := parseSelfLogin([]byte(`{"self_login":""}`)); err == nil {
		t.Error("empty self_login should error")
	}
}

func TestReadBeadsPrefix_parsesIssuePrefix(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("issue_prefix: zr\nsome_other: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readBeadsPrefix(dir)
	if err != nil {
		t.Fatalf("readBeadsPrefix error: %v", err)
	}
	if got != "zr" {
		t.Errorf("readBeadsPrefix = %q, want %q", got, "zr")
	}
}

func TestReadBeadsPrefix_missingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := readBeadsPrefix(dir); err == nil {
		t.Error("missing config.yaml should error")
	}
}

func TestReadBeadsPrefix_missingKey(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("some_other: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBeadsPrefix(dir); err == nil {
		t.Error("missing issue_prefix key should error")
	}
}

func TestPrecheck_prefixMismatch(t *testing.T) {
	// Set up a temp repo root with .beads/config.yaml containing prefix "wrong"
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("issue_prefix: wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// mismatch: config says "wrong" but we want "zr"
	if err := precheckPrefix(dir, "zr"); err == nil {
		t.Error("prefix mismatch should fail precheck")
	}
	// match: config says "wrong" and we want "wrong"
	if err := precheckPrefix(dir, "wrong"); err != nil {
		t.Errorf("matching prefix should pass, got %v", err)
	}
}
