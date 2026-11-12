package main

import (
	"os"
	"testing"
)

// reexecEnv makes the test binary behave as the pa-monitor command itself, so
// tests that need a real process (exit codes, stderr, no daemon) re-exec the
// already-built test binary instead of paying a `go build` per test.
const reexecEnv = "PA_MONITOR_TEST_REEXEC_AS_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// selfBinary returns the path of the running test binary and arranges for any
// child it starts to run as the real command.
func selfBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv(reexecEnv, "1")
	return exe
}
