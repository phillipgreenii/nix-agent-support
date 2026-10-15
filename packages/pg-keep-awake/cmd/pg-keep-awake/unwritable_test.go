package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("mode 0500 does not block root")
	}
}

// writeCommands is every state-changing command that writes under the state
// directory (release and resume only remove, so they are not listed).
var writeCommands = map[string][]string{
	"acquire": {"acquire", "--owner", "t", "--for", "1m"},
	"run":     {"run", "--owner", "t", "--", "true"},
	"pause":   {"pause"},
}

func TestUnwritableStateDirExits3(t *testing.T) {
	skipIfRoot(t)
	requireProcInfo(t)
	for name, args := range writeCommands {
		t.Run("read-only parent/"+name, func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_STATE_HOME", xdg)
			if err := os.Chmod(xdg, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(xdg, 0o700) })
			if code, _, stderr := runCLI(t, args...); code != 3 {
				t.Fatalf("exit = %d, want 3 (stderr %q)", code, stderr)
			}
		})
		t.Run("read-only state dir/"+name, func(t *testing.T) {
			sd := useStateDir(t)
			if _, err := lease.EnsureDir(sd); err != nil {
				t.Fatal(err)
			}
			// pause writes in the state directory, the others in leases/.
			for _, dir := range []string{sd, filepath.Join(sd, "leases")} {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			if code, _, stderr := runCLI(t, args...); code != 3 {
				t.Fatalf("exit = %d, want 3 (stderr %q)", code, stderr)
			}
		})
	}
}

func TestStateDirWithOpenModeExits3(t *testing.T) {
	sd := useStateDir(t)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "acquire", "--owner", "t", "--for", "1m"); code != 3 {
		t.Fatalf("exit = %d, want 3 (stderr %q)", code, stderr)
	}
}

func TestLockHeldTooLongExits3(t *testing.T) {
	sd := useStateDir(t)
	d, err := lease.EnsureDir(sd)
	if err != nil {
		t.Fatal(err)
	}
	lk, err := d.TakeLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	start := time.Now()
	if code, _, stderr := runCLI(t, "acquire", "--owner", "t", "--for", "1m"); code != 3 {
		t.Fatalf("exit = %d, want 3 (stderr %q)", code, stderr)
	}
	if took := time.Since(start); took < lease.LockTimeout || took > lease.LockTimeout+3*time.Second {
		t.Errorf("gave up after %v, want about the %v lock bound", took, lease.LockTimeout)
	}
}

func TestStateCommandsNeedNoKeeper(t *testing.T) {
	// No keeper process exists in the test; every state-changing command must
	// still succeed, writing only files.
	requireProcInfo(t)
	sd := useStateDir(t)
	steps := [][]string{
		{"acquire", "--owner", "t", "--for", "1m"},
		{"pause", "--for", "1m"},
		{"resume"},
		{"run", "--owner", "t", "--", "true"},
		{"release", "--owner", "t", "--all"},
	}
	for _, args := range steps {
		if code, _, stderr := runCLI(t, args...); code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
	if fileExists(filepath.Join(sd, "status.json")) {
		t.Error("a client command wrote keeper state")
	}
}
