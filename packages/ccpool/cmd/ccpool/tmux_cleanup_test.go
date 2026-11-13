package main

import (
	"context"
	"os/exec"
	"testing"
)

// tmuxServerUp reports whether a tmux server still answers on the -L socket.
func tmuxServerUp(socket string) bool {
	return exec.CommandContext(context.Background(), "tmux", "-L", socket, "list-sessions").Run() == nil
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
	_ = exec.CommandContext(context.Background(), "tmux", "-L", socket, "kill-server").Run()
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
