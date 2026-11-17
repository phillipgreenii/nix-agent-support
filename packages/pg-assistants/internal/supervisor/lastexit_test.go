package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/tmux"
)

func TestWriteLastExit(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	d := tmux.DeadInfo{Status: 3, Signal: 15, Tail: "starting\nerror: boom\n\n\n"}
	if err := WriteLastExit(stateDir, "example-a", d, 90*time.Second, at); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stateDir, "last-exit")
	path := filepath.Join(dir, "example-a.txt")
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"example-a",
		"2026-11-17T10:00:00Z",
		"status: 3\n",
		"signal: 15 (terminated)\n",
		"uptime: 1m30s\n",
		"starting\nerror: boom\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("last exit file lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "error: boom\n") {
		t.Errorf("last exit file does not end at the tail's last line:\n%q", got)
	}
}

// A second exit replaces the first; a pane that ended without a signal says so.
func TestWriteLastExitReplacesAndNoSignal(t *testing.T) {
	stateDir := t.TempDir()
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	if err := WriteLastExit(stateDir, "example-a", tmux.DeadInfo{Status: 3, Tail: "first"}, time.Second, at); err != nil {
		t.Fatal(err)
	}
	if err := WriteLastExit(stateDir, "example-a", tmux.DeadInfo{Status: 0, Tail: "second"}, 0, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(stateDir, "last-exit", "example-a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("second write did not replace the first:\n%s", got)
	}
	for _, want := range []string{"status: 0\n", "signal: none\n", "uptime: 0s\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("last exit file lacks %q:\n%s", want, got)
		}
	}
}

func TestWriteLastExitRefusesBadID(t *testing.T) {
	stateDir := t.TempDir()
	if err := WriteLastExit(stateDir, "../x", tmux.DeadInfo{}, 0, time.Now()); err == nil {
		t.Error(`WriteLastExit("../x") succeeded`)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("state dir holds %v after a refused write", names(entries))
	}
}
