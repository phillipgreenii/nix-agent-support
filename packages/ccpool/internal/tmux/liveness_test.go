package tmux

import (
	"os/exec"
	"testing"
)

// uses a throwaway -L socket so it never touches the user's tmux.
const testSocket = "ccpool-livetest"

func tmuxAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

func TestHasSession_trueForLive_falseAfterKill(t *testing.T) {
	tmuxAvailable(t)
	// ensure clean server
	_ = exec.Command("tmux", "-L", testSocket, "kill-server").Run()
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", testSocket, "kill-server").Run() })

	if HasSession(testSocket, "cc-live") {
		t.Fatal("HasSession true before creation")
	}
	if err := exec.Command("tmux", "-L", testSocket, "new-session", "-d", "-s", "cc-live", "sleep", "30").Run(); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !HasSession(testSocket, "cc-live") {
		t.Fatal("HasSession false for a live session")
	}
	_ = exec.Command("tmux", "-L", testSocket, "kill-session", "-t", "cc-live").Run()
	if HasSession(testSocket, "cc-live") {
		t.Fatal("HasSession true after kill")
	}
}

func TestHasSession_falseWhenNoServer(t *testing.T) {
	tmuxAvailable(t)
	_ = exec.Command("tmux", "-L", "ccpool-noserver", "kill-server").Run()
	if HasSession("ccpool-noserver", "cc-anything") {
		t.Fatal("HasSession true with no server running")
	}
}
