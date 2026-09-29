//go:build unix

// The process-group isolation this file provides mirrors
// internal/inputproc/procgroup_unix.go's own (identical rationale, same
// //go:build unix boundary this repo already uses for pa-monitor's
// internal/reexec syscall.Exec calls): this repo targets darwin and linux
// only, both implementing Setpgid and kill(2)'s negative-pid group
// semantics identically, so one implementation suffices. A separate copy in
// this package rather than an import of inputproc's — internal/pathspec
// does not otherwise depend on internal/inputproc, and P9's git-hardening
// gap (this packet) and P10's input-processor gap (also this packet, but
// package inputproc) are independent concerns that happen to need the same
// small OS primitive.

package pathspec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateProcessGroup makes cmd the leader of its own process group and aims
// a context-cancellation kill at that GROUP instead of just the process
// exec.CommandContext started. Without it, a git invocation this package
// treats as "killed at the deadline" can still hang the caller indefinitely:
// exec.CommandContext's default Cancel kills only the direct child, but
// CombinedOutput/Output read toward an EOF that cannot arrive until every
// process holding the inherited stdout/stderr write end — including a
// grandchild the direct child forked and left behind — has exited (measured
// at 30.25s against a 300ms deadline for the identical shape in
// internal/inputproc, pg2-15uhy). P9 treats this as part of the same
// hardening: a hostile PATH-shadowed "git" is exactly the kind of
// repo/env-controlled code that could fork a child deliberately to survive
// a naive single-process kill.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Replaces CommandContext's default Cancel (cmd.Process.Kill), which
	// only signals the single process it started. Cancel runs while Wait is
	// still blocked, so the child has not yet been reaped and -pid still
	// names only this command's own group.
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }
}

// killProcessGroup SIGKILLs every member of the group led by pid. ESRCH
// means the group is already empty, reported as os.ErrProcessDone because
// that is what an exec.Cmd.Cancel func must return to leave the command's
// own exit status intact rather than replacing it with a cancellation
// error (mirrors internal/inputproc/procgroup_unix.go's identical helper).
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
