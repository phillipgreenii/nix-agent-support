package wrap

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Group makes cmd start in its own process group and makes a context cancel
// (cmd must come from exec.CommandContext, and Group must be called before
// Start) send SIGTERM to the whole group, then SIGKILL to the group after grace.
// This replaces exec.CommandContext's SIGKILL-to-the-direct-child-only, so a
// non-exec wrapper such as timeout cannot orphan the payload. If cmd.WaitDelay is
// zero it is set to grace plus a second, so a descendant holding an output pipe
// cannot block Wait past the escalation.
func Group(cmd *exec.Cmd, grace time.Duration) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = grace + time.Second
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pgid := cmd.Process.Pid
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		time.AfterFunc(grace, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		return nil
	}
}
