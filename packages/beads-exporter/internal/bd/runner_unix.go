//go:build unix

package bd

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group and makes
// context cancellation kill the whole group, not just the direct child.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid signals the whole group. ESRCH (already gone) is fine.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
}
