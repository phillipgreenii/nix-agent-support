// Package subshell counts the shell children of a process.
//
// On darwin the count is computed in-process from the kern.proc.all sysctl so
// no pgrep process is launched (every launch is inspected by endpoint
// security). Other platforms keep the `pgrep -P <pid> -l` path.
package subshell

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Proc is one row of a process table snapshot. Comm is the kernel p_comm
// (at most 16 bytes), not the full argv[0].
type Proc struct {
	PID  int
	PPID int
	Comm string
}

// Counter counts shell children. The zero value uses the platform default.
type Counter struct {
	// RunPs, when set, supplies `pgrep -P <parent> -l` style output (tests).
	RunPs func(parent int) (string, error)
	// ListProcs, when set, supplies a process table snapshot (tests). It is
	// consulted only when RunPs is nil.
	ListProcs func() ([]Proc, error)
}

// isShell reports whether a process name (any directory prefix is stripped) is
// one of zsh, bash or sh.
func isShell(name string) bool {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "zsh", "bash", "sh":
		return true
	}
	return false
}

// CountShells counts the DIRECT children of parent (ppid == parent) whose
// p_comm basename is zsh, bash or sh. Grandchildren are not counted.
func CountShells(procs []Proc, parent int) int {
	n := 0
	for _, p := range procs {
		if p.PPID == parent && p.PID != parent && isShell(p.Comm) {
			n++
		}
	}
	return n
}

// DefaultRunPs uses `pgrep -P <parent> -l` to list direct children.
// An exit code of 1 (no matches) is translated to an empty list, not an error.
func DefaultRunPs(parent int) (string, error) {
	out, err := exec.CommandContext(context.Background(), "pgrep", "-P", fmt.Sprintf("%d", parent), "-l").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

// CountPgrepOutput applies the shell filter to `pgrep -l` output: the last
// field of each "pid name" line, directory prefix stripped, must be zsh, bash
// or sh.
func CountPgrepOutput(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if isShell(fields[len(fields)-1]) {
			n++
		}
	}
	return n
}

// Count returns the number of direct shell children of parent.
func (c *Counter) Count(parent int) (int, error) {
	if c.RunPs != nil {
		out, err := c.RunPs(parent)
		if err != nil {
			return 0, err
		}
		return CountPgrepOutput(out), nil
	}
	if c.ListProcs != nil {
		procs, err := c.ListProcs()
		if err != nil {
			return 0, err
		}
		return CountShells(procs, parent), nil
	}
	return defaultCount(parent)
}
