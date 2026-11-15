package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMain lets this test binary run as the real command: a test that sets
// PG_TASK_FOCUS_AS_MAIN re-executes it and gets main() with the signal handling.
func TestMain(m *testing.M) {
	if os.Getenv("PG_TASK_FOCUS_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// A daemon that cannot start exits with the serve-failed code and says why.
func TestServeFailureExitsWithItsOwnCode(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"listen_port": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "serve", "--config", cfg, "--data-dir", filepath.Join(dir, "data"))
	cmd.Env = append(os.Environ(), "PG_TASK_FOCUS_AS_MAIN=1")
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 7 {
		t.Fatalf("exit = %v, want 7\n%s", err, out)
	}
}
