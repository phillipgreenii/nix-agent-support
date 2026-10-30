package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
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

func aliveFacts(alive bool, err error, gotPid *int64, gotStart *string) registryFacts {
	return registryFacts{
		alive: func(pid int64, procStart string) (bool, error) {
			if gotPid != nil {
				*gotPid, *gotStart = pid, procStart
			}
			return alive, err
		},
		awaiting:     func(string) (bool, error) { return false, nil },
		lastActivity: func(string) (time.Time, bool, error) { return time.Time{}, false, nil },
	}
}

func TestRegistryVerdict_noMatchingSessionIsNotFound(t *testing.T) {
	dir := t.TempDir()
	writeRegFile(t, dir, 4242, "OTHER-SESSION", "busy")
	_, found, err := registryVerdictWith(dir, "MY-SESSION", "", registryWaitingFreshWindow, aliveFacts(true, nil, nil, nil))
	if found || err != nil {
		t.Errorf("found = %v, err = %v; want false, nil (no row matches the ClaudeSessionID)", found, err)
	}
}

func TestRegistryVerdict_deadProcessIsNotFound(t *testing.T) {
	dir := t.TempDir()
	writeRegFile(t, dir, 4242, "MY-SESSION", "busy")
	_, found, err := registryVerdictWith(dir, "MY-SESSION", "", registryWaitingFreshWindow, aliveFacts(false, nil, nil, nil))
	if found || err != nil {
		t.Errorf("found = %v, err = %v; want false, nil (the recorder says the process is not alive)", found, err)
	}
}

func TestRegistryVerdict_liveBusyRowIsActive(t *testing.T) {
	dir := t.TempDir()
	writeRegFile(t, dir, 4242, "MY-SESSION", "busy")
	var pid int64
	var start string
	v, found, err := registryVerdictWith(dir, "MY-SESSION", "", registryWaitingFreshWindow, aliveFacts(true, nil, &pid, &start))
	if !found || err != nil {
		t.Fatalf("found = %v, err = %v; want true (live matching row)", found, err)
	}
	if pid != 4242 {
		t.Errorf("liveness asked for pid %d, want 4242", pid)
	}
	if v.Activity.String() != "active" {
		t.Errorf("Activity = %q, want active", v.Activity.String())
	}
}

// TestRegistryVerdict_procStartIsPassedToTheRecorder pins the PID-reuse input:
// the row's start time reaches the recorder in its ctime-style layout, and the
// reuse decision itself is the recorder's.
func TestRegistryVerdict_procStartIsPassedToTheRecorder(t *testing.T) {
	dir := t.TempDir()
	writeProcStartRow(t, dir, 4242, "Wed Oct 14 07:51:50 2026")
	var pid int64
	var start string
	if _, _, err := registryVerdictWith(dir, "MY-SESSION", "", registryWaitingFreshWindow, aliveFacts(true, nil, &pid, &start)); err != nil {
		t.Fatal(err)
	}
	if start != "Wed Oct 14 07:51:50 2026" {
		t.Errorf("procStart = %q", start)
	}
}

func TestRegistryVerdict_missingDirIsNotFound(t *testing.T) {
	_, found, err := registryVerdictWith(filepath.Join(t.TempDir(), "does-not-exist"), "MY-SESSION", "", registryWaitingFreshWindow, aliveFacts(true, nil, nil, nil))
	if found || err != nil {
		t.Errorf("found = %v, err = %v; want false, nil (missing sessions dir)", found, err)
	}
}

// With the recorder down the verdict is an error, never a guess from files.
func TestRegistryVerdict_daemonOfflineIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeRegFile(t, dir, 4242, "MY-SESSION", "busy")
	_, found, err := registryVerdictWith(dir, "MY-SESSION", "", registryWaitingFreshWindow, aliveFacts(false, facts.ErrDaemonOffline, nil, nil))
	if found || !errors.Is(err, facts.ErrDaemonOffline) {
		t.Errorf("found = %v, err = %v; want false, daemon offline", found, err)
	}
}

func writeProcStartRow(t *testing.T, dir string, pid int, procStart string) {
	t.Helper()
	body := `{"pid":` + itoa(pid) + `,"sessionId":"MY-SESSION","status":"busy","procStart":"` + procStart + `"}`
	if err := os.WriteFile(filepath.Join(dir, itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
