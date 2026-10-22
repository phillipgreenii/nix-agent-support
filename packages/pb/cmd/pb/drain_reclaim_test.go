package main

import (
	"bytes"
	"testing"
)

func runReclaim(args ...string) error {
	cmd := newDrainReclaimCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestDrainReclaimCmd_requiredFlags(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--bead", "pg2-x", "--repo", "/abs/repo"}, // no --actor: the note write needs one (B-5)
		{"--bead", "pg2-x", "--actor", "a"},        // no --repo
		{"--repo", "/abs/repo", "--actor", "a"},    // no --bead
	} {
		if err := runReclaim(args...); err == nil {
			t.Errorf("args %v: expected required-flag error", args)
		}
	}
}

func TestDrainReclaimCmd_rejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"relative repo":   {"--bead", "pg2-x", "--repo", "relative/path", "--actor", "a"},
		"path-shaped id":  {"--bead", "../evil", "--repo", "/abs/repo", "--actor", "a"},
		"blank actor":     {"--bead", "pg2-x", "--repo", "/abs/repo", "--actor", " "},
		"zero git-timout": {"--bead", "pg2-x", "--repo", "/abs/repo", "--actor", "a", "--git-timeout", "0"},
	}
	for name, args := range cases {
		if err := runReclaim(args...); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
