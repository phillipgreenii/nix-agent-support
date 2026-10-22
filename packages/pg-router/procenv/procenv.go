// Package procenv holds the two helpers pg-router core and the command adapter
// share for the processes pg-router starts itself: the minimal base environment
// (BaseEnv) and process-group cancellation (Group). It imports nothing from
// pg-router and nothing from pg-wrap.
package procenv

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// basePath is the fixed PATH of the base environment.
const basePath = "/usr/bin:/bin:/usr/sbin:/sbin"

var baseKeep = []string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "TERM", "COLORTERM"}

// BaseEnv builds the minimal base environment for a process pg-router starts. source and
// the result are in os.Environ() form (NAME=value). It keeps HOME, USER, LOGNAME,
// TMPDIR, LANG, TERM, COLORTERM and each name in extras where they are set in
// source (an empty value counts as set), and adds PATH=/usr/bin:/bin:/usr/sbin:/sbin.
// Names match exactly, never by prefix. A PATH in extras is ignored: the base PATH
// is fixed. When a name repeats in source, the last value wins.
func BaseEnv(source []string, extras []string) []string {
	have := make(map[string]string, len(source))
	for _, kv := range source {
		if i := strings.IndexByte(kv, '='); i > 0 {
			have[kv[:i]] = kv[i+1:]
		}
	}
	var out []string
	seen := map[string]bool{"PATH": true}
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		if v, ok := have[name]; ok {
			out = append(out, name+"="+v)
		}
	}
	for _, n := range baseKeep {
		add(n)
	}
	for _, n := range extras {
		add(n)
	}
	return append(out, "PATH="+basePath)
}

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
