//go:build integration

package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

// The tests in this file settle whether x/gitclient's cancel path (a new
// process group via Setpgid, cancelled with kill(-pgid, SIGKILL)) can leave
// a SHELL STEP or other descendant of a wedged git running. They use the
// real gitclient against a fake git wrapper (no mock of the cancel
// mechanism). The wrapper shape is the one an earlier completion-marker
// assertion used ("sleep N; touch marker"), plus a backgrounded grandchild
// holding the inherited stdout pipe.

const groupKillWait = 10 * time.Second

// pidAlive reports whether pid still exists (signal 0 probe).
func pidAlive(pid int) bool { return syscall.Kill(pid, 0) != syscall.ESRCH }

// waitPIDFile polls for a file holding a pid, up to groupKillWait.
func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(groupKillWait)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file %s never appeared", path)
	return 0
}

// waitGone polls until pid no longer exists, up to groupKillWait.
func waitGone(pid int) bool {
	deadline := time.Now().Add(groupKillWait)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// writeTrailingStepGit writes a git wrapper that, for `reset --hard`,
// records its shell PID, backgrounds a grandchild subshell (which records
// its own PID and sleeps, holding the inherited pipes), then runs a
// FOREGROUND sleep (PID recorded, via a background-and-wait so the test can
// target it alone) followed by a trailing `touch marker` shell step. Every
// other invocation execs the real git.
func writeTrailingStepGit(t *testing.T) (script, dir string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	dir = t.TempDir()
	script = filepath.Join(dir, "git")
	content := fmt.Sprintf(`#!/bin/sh
if [ "$1 $2" = "reset --hard" ]; then
  echo $$ > %[1]s/shell.pid
  ( sleep 120 & echo $! > %[1]s/grandchild.pid; wait ) &
  sleep 120 &
  echo $! > %[1]s/sleep.pid
  wait $!
  touch %[1]s/marker
  exit 0
fi
exec "%[2]s" "$@"
`, dir, realGit)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("writing wrapper: %v", err)
	}
	return script, dir
}

// TestGitclientCancelKillsWholeGroup: canceling a wedged git through the
// real gitclient leaves no process of the group alive (shell, foreground
// sleep, grandchild subshell and its sleep) and the trailing `touch marker`
// shell step never runs.
func TestGitclientCancelKillsWholeGroup(t *testing.T) {
	wrapper, dir := writeTrailingStepGit(t)
	repo := initFixtureRepo(t, "wt-branch").Dir

	client, err := gitclient.New(context.Background(), repo, gitclient.WithGit(wrapper))
	if err != nil {
		t.Fatalf("gitclient.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.ResetHard(ctx) }()

	// Cancel only once the whole tree provably exists, so the assertion
	// cannot pass vacuously because the kill beat the wrapper's startup.
	pids := map[string]int{}
	for _, name := range []string{"shell", "sleep", "grandchild"} {
		pids[name] = waitPIDFile(t, filepath.Join(dir, name+".pid"))
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ResetHard error = %v, want context.Canceled", err)
		}
	case <-time.After(groupKillWait):
		t.Fatal("ResetHard did not return after cancel")
	}

	for name, pid := range pids {
		if !waitGone(pid) {
			t.Errorf("%s process %d still alive after cancel: group kill leaked it", name, pid)
		}
	}
	// With the shell dead no later step can run; confirm none did. The
	// shell's death is asserted above, so absence is final, not a race.
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Error("trailing shell step ran (marker exists) after cancel")
	}
}

// TestKillingOnlyTheSleepLetsTheTrailingStepRun is the control for the
// original marker: with the SAME wrapper and no gitclient, killing only the
// foreground sleep (not its group) lets the shell fall through to
// `touch marker`. Anything that ends the sleep early without ending the
// shell therefore produces the marker, which is why a marker assertion was
// not evidence of a group-kill leak.
func TestKillingOnlyTheSleepLetsTheTrailingStepRun(t *testing.T) {
	wrapper, dir := writeTrailingStepGit(t)

	cmd := exec.CommandContext(t.Context(), wrapper, "reset", "--hard")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	shellPID := waitPIDFile(t, filepath.Join(dir, "shell.pid"))
	sleepPID := waitPIDFile(t, filepath.Join(dir, "sleep.pid"))
	grandPID := waitPIDFile(t, filepath.Join(dir, "grandchild.pid"))
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })

	if err := syscall.Kill(sleepPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(groupKillWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
			_ = shellPID
			_ = grandPID
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("marker never appeared although only the sleep was killed")
}
