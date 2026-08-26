package main

import (
	"bytes"
	"testing"
)

func TestDrainIsolateCmd_requiredFlags(t *testing.T) {
	cmd := newDrainIsolateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected required-flag error")
	}
}

func TestDrainIsolateCmd_rejectsRelativeRepo(t *testing.T) {
	cmd := newDrainIsolateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--bead", "pg2-x", "--repo", "relative/path"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for a relative --repo (orchestrators must pass observed absolute roots)")
	}
}

func TestDrainIsolateCmd_rejectsPathShapedBeadID(t *testing.T) {
	// The bead id lands in a filesystem path and a branch ref; live ids contain
	// dots (.worktrees/pg2-4dz88.2.3 exists), so dots pass but separators and
	// bare dot-dirs must not.
	for _, bad := range []string{"../evil", "a/b", ".", ".."} {
		cmd := newDrainIsolateCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--bead", bad, "--repo", "/abs/repo"})
		if err := cmd.Execute(); err == nil {
			t.Errorf("--bead %q: expected rejection", bad)
		}
	}
}
