package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRegFile writes a minimal Claude session-registry JSON keyed by pid.
func writeRegFile(t *testing.T, dir string, pid int, sessionID, status string) {
	t.Helper()
	body := `{"pid":` + itoa(pid) + `,"sessionId":"` + sessionID + `","status":"` + status + `"}`
	if err := os.WriteFile(filepath.Join(dir, itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestRegistryVerdict_noMatchingSessionIsNotFound(t *testing.T) {
	dir := t.TempDir()
	writeRegFile(t, dir, os.Getpid(), "OTHER-SESSION", "busy")
	_, found := registryVerdict(dir, "MY-SESSION", "", registryWaitingFreshWindow)
	if found {
		t.Error("found = true, want false (no row matches the ClaudeSessionID)")
	}
}

func TestRegistryVerdict_deadPidIsNotFound(t *testing.T) {
	dir := t.TempDir()
	// PID 1 is init; on a normal host the test process cannot signal it, but use
	// an unused high pid to be deterministic-dead.
	writeRegFile(t, dir, 2147480000, "MY-SESSION", "busy")
	_, found := registryVerdict(dir, "MY-SESSION", "", registryWaitingFreshWindow)
	if found {
		t.Error("found = true, want false (matched row's pid is dead)")
	}
}

func TestRegistryVerdict_liveBusyRowIsActive(t *testing.T) {
	dir := t.TempDir()
	// Use this test process's own pid so PidAlive is true.
	writeRegFile(t, dir, os.Getpid(), "MY-SESSION", "busy")
	v, found := registryVerdict(dir, "MY-SESSION", "", registryWaitingFreshWindow)
	if !found {
		t.Fatal("found = false, want true (live matching row)")
	}
	if v.Activity.String() != "active" {
		t.Errorf("Activity = %q, want active", v.Activity.String())
	}
}

func TestRegistryVerdict_missingDirIsNotFound(t *testing.T) {
	_, found := registryVerdict(filepath.Join(t.TempDir(), "does-not-exist"), "MY-SESSION", "", registryWaitingFreshWindow)
	if found {
		t.Error("found = true, want false (missing sessions dir)")
	}
}
