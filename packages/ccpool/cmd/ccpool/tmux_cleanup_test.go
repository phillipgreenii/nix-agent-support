package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// tmuxServerUp reports whether a tmux server still answers on the -L socket.
func tmuxServerUp(socket string) bool {
	return exec.Command("tmux", "-L", socket, "list-sessions").Run() == nil
}

// killTmuxServer kills the test's own tmux server on socket and fails t when a
// server still answers afterwards (a leaked server outlives the test run: an
// orphaned `cc-<hash>` server was found running for days, pg2-ycknzn). An absent
// server is fine. It never matches by pattern, only by the exact -L socket.
func killTmuxServer(t *testing.T, socket string) {
	t.Helper()
	if socket == "" {
		return
	}
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	if tmuxServerUp(socket) {
		t.Errorf("tmux server still running on socket %q after kill-server", socket)
	}
}

// registerTmuxCleanup registers killTmuxServer(socket) with t.Cleanup. Call it
// BEFORE anything can start the server, so every exit path (success, t.Fatal,
// panic recovered by the test runner, timeout) tears the server down.
func registerTmuxCleanup(t *testing.T, socket string) {
	t.Helper()
	t.Cleanup(func() { killTmuxServer(t, socket) })
}

func TestKillTmuxServer_removesServerAndTolerantOfAbsent(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Short unique socket per run: t.TempDir's basename, never a shared name.
	socket := "ccclean-" + filepath.Base(t.TempDir())
	if len(socket) > 40 {
		socket = socket[:40]
	}
	killTmuxServer(t, socket) // no server yet: must not fail
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "cc-x", "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	registerTmuxCleanup(t, socket)
	if !tmuxServerUp(socket) {
		t.Fatal("server should be up before cleanup")
	}
	killTmuxServer(t, socket)
	if tmuxServerUp(socket) {
		t.Fatal("server still up after killTmuxServer")
	}
}
