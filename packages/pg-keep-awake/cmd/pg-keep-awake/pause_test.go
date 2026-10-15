package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func readPause(t *testing.T, sd string) *lease.PauseFile {
	t.Helper()
	d, err := lease.CheckDir(sd)
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.ReadPause()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPauseWithoutForHasNullUntil(t *testing.T) {
	sd := useStateDir(t)
	if code, stdout, stderr := runCLI(t, "pause"); code != 0 || stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	p := readPause(t, sd)
	if p.Until != nil {
		t.Errorf("until = %v, want null", p.Until)
	}
	if p.ByPID != os.Getppid() {
		t.Errorf("by_pid = %d, want the parent pid %d", p.ByPID, os.Getppid())
	}
	if time.Since(p.CreatedAt) > time.Minute {
		t.Errorf("created_at = %v", p.CreatedAt)
	}
	fi, err := os.Stat(filepath.Join(sd, "pause.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("pause.json: %v, %v", fi, err)
	}
}

func TestPauseForSetsUntil(t *testing.T) {
	sd := useStateDir(t)
	before := time.Now()
	if code, _, stderr := runCLI(t, "pause", "--for", "30m"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	p := readPause(t, sd)
	if p.Until == nil || p.Until.Before(before.Add(29*time.Minute)) || p.Until.After(time.Now().Add(31*time.Minute)) {
		t.Errorf("until = %v, want about now+30m", p.Until)
	}
}

func TestResumeRemovesPauseAndTouchesNoLease(t *testing.T) {
	sd := useStateDir(t)
	if code, _, _ := runCLI(t, "acquire", "--owner", "t", "--for", "5m"); code != 0 {
		t.Fatal("acquire failed")
	}
	before := readLeaseFile(t, sd, "t.default")
	if code, _, _ := runCLI(t, "pause"); code != 0 {
		t.Fatal("pause failed")
	}
	if !fileExists(filepath.Join(sd, "pause.json")) {
		t.Fatal("pause wrote no pause.json")
	}
	if got := readLeaseFile(t, sd, "t.default"); !got.ExpiresAt.Equal(*before.ExpiresAt) {
		t.Error("pause changed a lease")
	}
	for i := 0; i < 2; i++ { // the second resume has nothing to remove
		if code, _, stderr := runCLI(t, "resume"); code != 0 {
			t.Fatalf("resume #%d: exit %d, stderr %q", i, code, stderr)
		}
	}
	if fileExists(filepath.Join(sd, "pause.json")) {
		t.Error("resume left pause.json")
	}
	if !fileExists(leasePath(sd, "t.default")) {
		t.Error("resume removed a lease")
	}
}

func TestResumeWithoutStateDir(t *testing.T) {
	sd := useStateDir(t)
	if code, _, stderr := runCLI(t, "resume"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(sd); err == nil {
		t.Error("resume created the state directory")
	}
}

func TestPauseResumeUsageErrors(t *testing.T) {
	useStateDir(t)
	cases := map[string][]string{
		"unparsable for": {"pause", "--for", "later"},
		"zero for":       {"pause", "--for", "0s"},
		"negative for":   {"pause", "--for", "-1m"},
		"pause arg":      {"pause", "extra"},
		"resume arg":     {"resume", "extra"},
		"resume flag":    {"resume", "--for", "1m"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, _, stderr := runCLI(t, args...); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, stderr)
			}
		})
	}
}
