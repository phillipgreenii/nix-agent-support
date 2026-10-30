//go:build integration

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBinaryPrintsVersion builds the real binary and runs it as a subprocess,
// which makes it integration tier (V-4).
func TestBinaryPrintsVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pg-claude-code-recorder")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "pg-claude-code-recorder ") {
		t.Fatalf("unexpected output %q", out)
	}
}
