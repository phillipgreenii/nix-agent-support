//go:build integration || smoke

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildPgRouterBinary compiles the REAL cmd/pg-router main package this test file
// belongs to (`go build -o <tmp> .`, run with this package's own directory as
// the build's working directory) — the actual artifact operators run, not a
// package-level fake.
func buildPgRouterBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pg-router")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/pg-router: %v\n%s", err, out)
	}
	return bin
}
