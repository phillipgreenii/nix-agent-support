//go:build integration

package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Starts a real tmux server, so it sits behind the integration tag (V-4).
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
	if out, err := exec.CommandContext(t.Context(), "tmux", "-L", socket, "new-session", "-d", "-s", "cc-x", "sleep", "30").CombinedOutput(); err != nil {
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
