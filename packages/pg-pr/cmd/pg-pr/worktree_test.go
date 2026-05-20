package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestWorktreeListEmptyHuman verifies the human-readable list output on a
// non-existent worktree root prints the expected "No PR worktrees" line
// and exits zero.
func TestWorktreeListEmptyHuman(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"worktree", "list", "--worktree-root", tmp + "/missing"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "No PR worktrees under") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

// TestWorktreeListEmptyJSON verifies --json emits an empty JSON list on
// an empty worktree root.
func TestWorktreeListEmptyJSON(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"worktree", "list", "--worktree-root", tmp + "/missing", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "[]" {
		t.Fatalf("expected empty JSON list, got %q", got)
	}
}

// TestWorktreeAddRejectsInvalidPR verifies argument validation runs
// before any git/gh calls.
func TestWorktreeAddRejectsInvalidPR(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"worktree", "add", "not-a-number", "--worktree-root", tmp})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error for invalid PR id")
	}
}
