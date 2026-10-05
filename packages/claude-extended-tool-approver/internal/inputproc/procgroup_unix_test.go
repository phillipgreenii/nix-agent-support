//go:build unix

package inputproc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
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
	cmd := exec.Command("/usr/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	time.Sleep(300 * time.Millisecond) // let it exit; it stays a zombie until Wait

	err := killProcessGroup(cmd.Process.Pid)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killProcessGroup() on an exited, unreaped group = %v, want nil or os.ErrProcessDone (never %v)", err, syscall.EPERM)
	}
}
