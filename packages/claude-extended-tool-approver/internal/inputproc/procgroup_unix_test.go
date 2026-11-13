//go:build unix

package inputproc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestKillProcessGroup_ExitedUnreapedGroup_IsProcessDone pins the darwin quirk
// killProcessGroup's doc describes (pg2-bri3b): once every member of the group
// has exited but the leader is still an unreaped zombie, macOS answers
// kill(-pgid) with EPERM (linux succeeds). That must surface as
// os.ErrProcessDone — what exec.Cmd.Cancel needs to leave a successful exit
// intact — never as a raw EPERM, which made Wait fail with `exec: canceling
// Cmd: operation not permitted` after the processor had already finished.
//
// The child is deliberately NOT reaped (no Wait) until the kill has been tried.
// If it has not exited yet by then (a very loaded machine) the kill simply
// succeeds and returns nil, which the assertion also accepts, so this can only
// become vacuous under load, never flaky.
func TestKillProcessGroup_ExitedUnreapedGroup_IsProcessDone(t *testing.T) {
	// Resolve via PATH: /usr/bin/true does not exist on NixOS or in the nix sandbox.
	truePath, lerr := exec.LookPath("true")
	if lerr != nil {
		t.Skipf("no `true` executable on PATH: %v", lerr)
	}
	cmd := exec.CommandContext(context.Background(), truePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The child's stdout is the write end of a pipe the parent does not
	// hold: reading to EOF returns exactly when the child has exited, while
	// it stays an unreaped zombie (no Wait yet). This replaces a sleep.
	pr, pw, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("pipe: %v", perr)
	}
	defer func() { _ = pr.Close() }()
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = pw.Close()
	defer func() { _ = cmd.Wait() }()

	_, _ = io.Copy(io.Discard, pr) // EOF: the child has exited; it stays a zombie until Wait

	err := killProcessGroup(cmd.Process.Pid)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killProcessGroup() on an exited, unreaped group = %v, want nil or os.ErrProcessDone (never %v)", err, syscall.EPERM)
	}
}
