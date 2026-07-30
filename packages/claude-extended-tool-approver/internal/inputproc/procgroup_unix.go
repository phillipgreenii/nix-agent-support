//go:build unix

// The two calls this package cannot make portably — creating a process group and
// signalling one — are isolated here, behind the same //go:build unix boundary
// pa-monitor's internal/reexec puts syscall.Exec behind. This repo targets darwin
// and linux; both implement Setpgid and kill(2)'s negative-pid group semantics
// identically, so there is one implementation rather than a per-GOOS pair.

package inputproc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateProcessGroup makes the input processor the leader of its own process
// group and aims the deadline kill at that GROUP instead of the single child.
// Without it the deadline bounds nothing: exec.CommandContext kills only the
// process it started, so a grandchild that inherited the stdout write end keeps
// cmd.Output() reading toward an EOF that cannot arrive until the grandchild
// exits — measured at 30.25s against a 300ms deadline (pg2-15uhy).
//
// Setpgid gives the child a pgid equal to its own pid, so the negative-pid kill
// can only reach processes the processor itself started; the hook keeps its
// inherited group and therefore cannot signal itself, which is the failure mode
// this pattern is worth spelling out to avoid. The cost is that a terminal signal
// sent to the hook's group no longer reaches the processor — bounded anyway by the
// deadline and waitGrace, so nothing waits on it indefinitely.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Replaces CommandContext's default Cancel (cmd.Process.Kill). Cancel runs
	// while Wait is still blocked, so the child has not been reaped and -pid is
	// still the processor's group and nothing else's.
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }
}

// reapProcessGroup kills whatever the processor left running behind it. It is
// called ONLY on the path where the parent observed a live holder of the output
// pipe, and that is also what makes it safe: a group with a living member cannot
// have had its pgid recycled, whereas an unconditional kill after the child is
// reaped could in principle name a group that reused the pid.
func reapProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = killProcessGroup(cmd.Process.Pid)
}

// killProcessGroup SIGKILLs every member of the group led by pid. ESRCH means the
// group is already empty, reported as os.ErrProcessDone because that is what an
// exec.Cmd.Cancel func must return to leave the command's own exit status intact
// rather than replacing it with a cancellation error.
func killProcessGroup(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
