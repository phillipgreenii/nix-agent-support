package keeper

import (
	"fmt"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// Claude Code's own sleep inhibitor (bead pg2-0n6og, ADR 0100 decision 8).
//
// Claude Code (2.1.303, confirmed in the bundled JavaScript of bin/.claude-wrapped)
// holds the Mac awake while a turn is busy with
//
//	child_process.spawn("caffeinate", ["-i","-t","300"], {env: {..., PATH: "/usr/bin:/bin:/usr/sbin:/sbin"}})
//
// restarted every 240 s while busy. The child environment's PATH is replaced
// by that constant, so a `caffeinate` shim earlier on the user's PATH is never
// consulted: the real /usr/bin/caffeinate runs, with argv[0] "caffeinate", and
// no lease is ever recorded for it. The keeper therefore governs it by
// identity instead: an external `caffeinate` holder whose argv is exactly
// Claude Code's and whose parent is a `claude` process is "managed", and is
// stopped when the battery gate is released.

const (
	// ClaudeCaffeinateExe is the binary Claude Code's inhibitor runs.
	ClaudeCaffeinateExe = "/usr/bin/caffeinate"
	// claudeTermWait bounds the wait after SIGTERM before SIGKILL, and after
	// SIGKILL before giving up; the keeper tick must not stall on a holder.
	claudeTermWait = time.Second
)

// ClaudeHolders recognises and reaps Claude Code's own caffeinate inhibitors.
// Recognition is deliberately narrow, so a caffeinate that someone else
// started is never touched: every test below MUST hold, checked against the
// live process table at the moment of use.
//
//  1. the holder is a running, non-zombie process with a parent other than
//     init;
//  2. its executable is Exe (default /usr/bin/caffeinate);
//  3. its argv is exactly `caffeinate -i -t <seconds>` (Claude Code's
//     request; a user's `caffeinate -s ...`, `-w <pid>` and the keeper's own
//     `caffeinate -w <anchor>` never match);
//  4. its parent is running and its argv[0] (or executable name) is `claude`.
//
// A process owned by another user fails the argv read and so never matches.
type ClaudeHolders struct {
	Procs ProcControl
	// Exe is the caffeinate binary a managed holder runs; empty means
	// ClaudeCaffeinateExe. Tests point it at a copy of the test binary.
	Exe string
}

func (c ClaudeHolders) exe() string {
	if c.Exe != "" {
		return c.Exe
	}
	return ClaudeCaffeinateExe
}

// identify reports whether pid is Claude Code's inhibitor and, if so, a
// reference (pid and start time) that pins the process for the later signal.
func (c ClaudeHolders) identify(pid int) (lease.ProcRef, bool) {
	if pid <= 1 {
		return lease.ProcRef{}, false
	}
	st := c.Procs.Probe(pid)
	if !st.Exists || st.Zombie || st.PPID <= 1 {
		return lease.ProcRef{}, false
	}
	info, err := c.Procs.Info(pid)
	if err != nil || !sameExecutable(info.Exe, c.exe()) || !isClaudeInhibitorArgv(info.Argv) {
		return lease.ProcRef{}, false
	}
	pst := c.Procs.Probe(st.PPID)
	if !pst.Exists || pst.Zombie {
		return lease.ProcRef{}, false
	}
	pinfo, err := c.Procs.Info(st.PPID)
	if err != nil || !isClaudeProcess(pinfo) {
		return lease.ProcRef{}, false
	}
	return lease.ProcRef{PID: pid, StartUS: st.StartUS}, true
}

// Mark returns ext with ManagedBy set on every recognised Claude Code
// inhibitor. The input is not modified.
func (c ClaudeHolders) Mark(ext []lease.StatusExternal) []lease.StatusExternal {
	out := make([]lease.StatusExternal, len(ext))
	copy(out, ext)
	for i := range out {
		out[i].ManagedBy = ""
		if out[i].Process != "caffeinate" {
			continue
		}
		if _, ok := c.identify(out[i].PID); ok {
			out[i].ManagedBy = lease.ManagedByClaude
		}
	}
	return out
}

// ClaudeReap is the outcome of one Reap.
type ClaudeReap struct {
	// Reaped lists the pids that are gone after the signal.
	Reaped []int
	// Errs lists holders that could not be stopped.
	Errs []error
}

// Reap stops every recognised Claude Code inhibitor in ext: SIGTERM, then
// SIGKILL if it is still there after claudeTermWait. Each holder is
// re-identified immediately before each signal and only signalled while its
// start time still matches, so a recycled pid is never hit. Claude Code
// notices the exit and re-arms on its own 240 s timer while a turn is busy;
// the next tick under a released gate reaps the replacement.
func (c ClaudeHolders) Reap(ext []lease.StatusExternal) ClaudeReap {
	var res ClaudeReap
	for _, e := range ext {
		if e.Process != "caffeinate" {
			continue
		}
		ref, ok := c.identify(e.PID)
		if !ok {
			continue
		}
		if err := c.stop(ref); err != nil {
			res.Errs = append(res.Errs, err)
			continue
		}
		res.Reaped = append(res.Reaped, ref.PID)
	}
	return res
}

func (c ClaudeHolders) stop(ref lease.ProcRef) error {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		st := c.Procs.Probe(ref.PID)
		if !st.Exists || st.Zombie {
			return nil
		}
		if st.StartUS != ref.StartUS {
			return nil // the pid was recycled: the holder we meant is gone
		}
		if err := c.Procs.Signal(ref.PID, sig); err != nil {
			if st := c.Procs.Probe(ref.PID); !st.Exists || st.Zombie {
				return nil
			}
			return fmt.Errorf("claude holder %d: signal %v: %w", ref.PID, sig, err)
		}
		if c.Procs.WaitExit(ref.PID, claudeTermWait) {
			return nil
		}
	}
	return fmt.Errorf("claude holder %d: still running after SIGKILL", ref.PID)
}

// isClaudeInhibitorArgv reports whether argv is exactly
// `caffeinate -i -t <positive seconds>`.
func isClaudeInhibitorArgv(argv []string) bool {
	if len(argv) != 4 || filepath.Base(argv[0]) != "caffeinate" || argv[1] != "-i" || argv[2] != "-t" {
		return false
	}
	n, err := strconv.Atoi(argv[3])
	return err == nil && n > 0
}

// isClaudeProcess reports whether info is a Claude Code process: the launcher
// runs `exec -a claude <real binary>`, so argv[0] is "claude" while the
// executable is the real binary (.claude-wrapped, or a versioned file).
func isClaudeProcess(info lease.ProcInfo) bool {
	if len(info.Argv) > 0 && filepath.Base(info.Argv[0]) == "claude" {
		return true
	}
	return info.Name == "claude"
}

// sameExecutable compares two executable paths, resolving symlinks when the
// files exist (the kernel may record /private/var for a /var spelling).
func sameExecutable(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	if filepath.Clean(got) == filepath.Clean(want) {
		return true
	}
	g, gerr := filepath.EvalSymlinks(got)
	w, werr := filepath.EvalSymlinks(want)
	return gerr == nil && werr == nil && g == w
}
