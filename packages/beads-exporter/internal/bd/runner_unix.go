//go:build unix

package bd

import (
	"os/exec"
	"syscall"
)

// killProcess is the signalling primitive; a variable only so tests can
// inject ESRCH/EPERM deterministically (see runner_test.go).
var killProcess = syscall.Kill

// configureProcessGroup puts the child in its own process group and makes
// context cancellation kill the whole group, not just the direct child.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid signals the whole group. ESRCH (already gone) is fine.
		if err := killProcess(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
}
