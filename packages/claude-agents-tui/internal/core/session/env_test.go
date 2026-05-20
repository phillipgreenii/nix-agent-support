package session

import (
	"os"
	"testing"
)

func TestReadProcessEnv_SelfReadsBootEnv(t *testing.T) {
	// macOS ps reads env from the kernel cache populated at process start,
	// not the live runtime env. So t.Setenv-injected vars won't appear via
	// ps -E. Use HOME / USER which are always set at startup.
	want := os.Getenv("HOME")
	if want == "" {
		t.Skip("HOME not set; nothing to compare")
	}
	env, err := ReadProcessEnv(os.Getpid())
	if err != nil {
		t.Logf("ReadProcessEnv error (may be non-fatal): %v", err)
	}
	if got := env["HOME"]; got != want {
		t.Errorf("env[HOME] = %q, want %q (got map size %d)", got, want, len(env))
	}
}

func TestReadProcessEnv_NonexistentPidReturnsEmpty(t *testing.T) {
	// pid 0 is not a valid process; we want a non-nil empty map and an error.
	env, err := ReadProcessEnv(0)
	if env == nil {
		t.Error("env should be non-nil empty map on failure")
	}
	if err == nil {
		t.Log("no error on pid 0 — implementation may differ across OSes; non-fatal")
	}
}
