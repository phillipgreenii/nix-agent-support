package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

func tmuxAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

// TestHasSession_trueForLive_falseAfterKill drives Client.HasSession against a
// stateful fake runner standing in for a tmux server, so no real tmux starts.
func TestHasSession_trueForLive_falseAfterKill(t *testing.T) {
	live := false
	c := &Client{Socket: "ccpool-livetest", run: func(args ...string) ([]byte, error) {
		switch args[2] {
		case "has-session":
			if !live {
				return nil, errors.New("can't find session: cc-live")
			}
			return nil, nil
		case "display-message":
			return []byte("0\n"), nil
		case "kill-session":
			live = false
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected tmux call %v", args)
	}}

	if c.HasSession("cc-live") {
		t.Fatal("HasSession true before creation")
	}
	live = true
	if !c.HasSession("cc-live") {
		t.Fatal("HasSession false for a live session")
	}
	if _, err := c.tmux("kill-session", "-t", "cc-live"); err != nil {
		t.Fatal(err)
	}
	if c.HasSession("cc-live") {
		t.Fatal("HasSession true after kill")
	}
}

func TestHasSession_falseWhenNoServer(t *testing.T) {
	tmuxAvailable(t)
	_ = exec.CommandContext(t.Context(), "tmux", "-L", "ccpool-noserver", "kill-server").Run()
	if HasSession("ccpool-noserver", "cc-anything") {
		t.Fatal("HasSession true with no server running")
	}
}
