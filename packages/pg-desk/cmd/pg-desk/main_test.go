package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestVersionCommand proves the one real (non-stub) command wired in this
// packet actually runs and prints something.
func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	cmd, _, err := rootCmd.Find([]string{"version"})
	if err != nil {
		t.Fatalf("rootCmd has no version subcommand: %v", err)
	}
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out.String(), "pg-desk") {
		t.Fatalf("expected version output to mention pg-desk, got %q", out.String())
	}
}
