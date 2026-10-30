package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeControl writes an executable that records its arguments and stdin
// into dir and exits with the given code (stderr text first).
func writeFakeControl(t *testing.T, dir string, exit string, stderr string) string {
	t.Helper()
	p := filepath.Join(dir, "fake-control")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + dir + "/args'\ncat > '" + dir + "/stdin'\n"
	if stderr != "" {
		script += "echo '" + stderr + "' >&2\n"
	}
	script += "exit " + exit + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestControlDelivererSubmitsThroughTheCommand runs the real exec path against a
// stand-in executable: the nudge text is the command's stdin and the target is
// the pid.
func TestControlDelivererSubmitsThroughTheCommand(t *testing.T) {
	dir := t.TempDir()
	d := &controlDeliverer{run: execRunner, command: writeFakeControl(t, dir, "0", "")}
	if err := d.Deliver(context.Background(), 4321, "continue please"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if got := strings.TrimSpace(string(args)); got != "submit pid:4321" {
		t.Errorf("args = %q", got)
	}
	if string(stdin) != "continue please" {
		t.Errorf("stdin = %q", stdin)
	}
}

// TestControlDelivererReportsTheCommandsRefusal: a refusal keeps the command's
// own error name, so the nudge history says why (no recorder is involved).
func TestControlDelivererReportsTheCommandsRefusal(t *testing.T) {
	dir := t.TempDir()
	d := &controlDeliverer{run: execRunner, command: writeFakeControl(t, dir, "4", "error: no_control_mod")}
	err := d.Deliver(context.Background(), 4321, "hi")
	if err == nil || !strings.Contains(err.Error(), "no_control_mod") || !strings.Contains(err.Error(), "exit 4") {
		t.Fatalf("err = %v", err)
	}
}

func TestControlDelivererMissingCommandIsAnError(t *testing.T) {
	d := &controlDeliverer{run: execRunner, command: filepath.Join(t.TempDir(), "absent")}
	if err := d.Deliver(context.Background(), 1, "x"); err == nil {
		t.Fatal("want an error for a missing command")
	}
}
