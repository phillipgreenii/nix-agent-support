package signal_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// fakePsRun returns a RunCmd that responds to:
//   - `ps -A -o pid,comm`     → header + one line per (pid, comm) in procs
//   - `ps -o ppid= -p <pid>`  → the parent PID for <pid>, or empty
type fakeProc struct {
	pid    int
	comm   string
	parent int
}

func fakePsRun(procs []fakeProc) func(context.Context, string, ...string) ([]byte, error) {
	parent := func(pid int) (int, bool) {
		for _, p := range procs {
			if p.pid == pid {
				return p.parent, true
			}
		}
		return 0, false
	}
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ps" {
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
		// `ps -A -o pid,comm` (cmux server enumeration)
		if len(args) >= 3 && args[0] == "-A" && args[1] == "-o" && args[2] == "pid,comm" {
			var sb strings.Builder
			sb.WriteString("  PID COMMAND\n")
			for _, p := range procs {
				sb.WriteString(fmt.Sprintf("%5d %s\n", p.pid, p.comm))
			}
			return []byte(sb.String()), nil
		}
		// `ps -o ppid= -p <pid>` (parent walking)
		if len(args) == 4 && args[0] == "-o" && args[1] == "ppid=" && args[2] == "-p" {
			var pid int
			if _, err := fmt.Sscanf(args[3], "%d", &pid); err != nil {
				return nil, fmt.Errorf("bad pid arg: %q", args[3])
			}
			ppid, ok := parent(pid)
			if !ok {
				return []byte(""), nil
			}
			return []byte(fmt.Sprintf("%d\n", ppid)), nil
		}
		return nil, fmt.Errorf("unexpected args: %v", args)
	}
}

// TestCmuxDetect_PidWithCmuxAncestorReturnsTrue: a session whose process tree
// includes a "cmux" comm process is detected as in-cmux. This is the
// post-fix behaviour (formerly gated on the daemon's own CMUX_WORKSPACE_ID
// env, which broke detection whenever the daemon was launchd-started).
func TestCmuxDetect_PidWithCmuxAncestorReturnsTrue(t *testing.T) {
	// 5000 (cmux server) -> 5001 (shell) -> 5002 (claude session)
	procs := []fakeProc{
		{pid: 5000, comm: "cmux", parent: 1},
		{pid: 5001, comm: "zsh", parent: 5000},
		{pid: 5002, comm: "claude", parent: 5001},
	}
	sig := &signal.CmuxSignaler{RunCmd: fakePsRun(procs)}
	if !sig.Detect(5002) {
		t.Error("Detect(5002) = false, want true (5002 has cmux server 5000 as grandparent)")
	}
}

// TestCmuxDetect_PidWithoutCmuxAncestorReturnsFalse: a session running
// outside cmux (e.g. plain Terminal.app shell) yields false even when other
// cmux servers exist elsewhere on the box.
func TestCmuxDetect_PidWithoutCmuxAncestorReturnsFalse(t *testing.T) {
	procs := []fakeProc{
		{pid: 5000, comm: "cmux", parent: 1},   // someone else's cmux
		{pid: 5001, comm: "zsh", parent: 5000}, // their shell
		{pid: 7000, comm: "iterm", parent: 1},  // our terminal — no cmux ancestor
		{pid: 7001, comm: "zsh", parent: 7000},
		{pid: 7002, comm: "claude", parent: 7001},
	}
	sig := &signal.CmuxSignaler{RunCmd: fakePsRun(procs)}
	if sig.Detect(7002) {
		t.Error("Detect(7002) = true, want false (no cmux server in its ancestry)")
	}
}

// TestCmuxDetect_DaemonOutsideCmuxStillWorks: regression test for the original
// bug. The daemon's own CMUX_WORKSPACE_ID is unset (LaunchAgent context), but
// detection still resolves because ps -A enumerates cmux servers globally.
func TestCmuxDetect_DaemonOutsideCmuxStillWorks(t *testing.T) {
	procs := []fakeProc{
		{pid: 5000, comm: "cmux", parent: 1},
		{pid: 5001, comm: "zsh", parent: 5000},
		{pid: 5002, comm: "claude", parent: 5001},
	}
	sig := &signal.CmuxSignaler{
		RunCmd:    fakePsRun(procs),
		LookupEnv: stubEnv(map[string]string{}), // daemon NOT in cmux
	}
	if !sig.Detect(5002) {
		t.Error("Detect(5002) = false, want true (regression: daemon outside cmux must still detect)")
	}
}

// TestCmuxFindCmuxServerAncestor_ReturnsServerPid: exposes which cmux server
// PID owns the target session — the poller needs this to look up bridge
// status for TerminalHost enrichment.
func TestCmuxFindCmuxServerAncestor_ReturnsServerPid(t *testing.T) {
	procs := []fakeProc{
		{pid: 5000, comm: "cmux", parent: 1},
		{pid: 5001, comm: "zsh", parent: 5000},
		{pid: 5002, comm: "claude", parent: 5001},
	}
	sig := &signal.CmuxSignaler{RunCmd: fakePsRun(procs)}
	server, ok := sig.FindCmuxServerAncestor(5002)
	if !ok {
		t.Fatal("FindCmuxServerAncestor = !ok, want ok=true")
	}
	if server != 5000 {
		t.Errorf("FindCmuxServerAncestor = %d, want 5000", server)
	}
}

// TestCmuxFindCmuxServerAncestor_NoCmuxReturnsZero: when no cmux servers run
// at all, walking ancestry shouldn't fabricate a match.
func TestCmuxFindCmuxServerAncestor_NoCmuxReturnsZero(t *testing.T) {
	procs := []fakeProc{
		{pid: 7000, comm: "iterm", parent: 1},
		{pid: 7001, comm: "zsh", parent: 7000},
		{pid: 7002, comm: "claude", parent: 7001},
	}
	sig := &signal.CmuxSignaler{RunCmd: fakePsRun(procs)}
	if _, ok := sig.FindCmuxServerAncestor(7002); ok {
		t.Error("FindCmuxServerAncestor = ok, want !ok (no cmux processes exist)")
	}
}

// TestCmuxFindCmuxServerAncestor_AcceptsPathPrefixedComm regresses a real
// user-reported bug: on macOS the cmux .app bundle's comm column is the
// absolute path "/nix/store/.../cmux.app/Contents/MacOS/cmux" rather than
// the bare basename. The detector must take the path's basename and match
// on that; otherwise every cmux session shows up as "unknown" terminal.
func TestCmuxFindCmuxServerAncestor_AcceptsPathPrefixedComm(t *testing.T) {
	procs := []fakeProc{
		{pid: 1623, comm: "/nix/store/13m0jxrl2i6p71zcd998xf77bpz8zyjn-cmux-0.64.10/Applications/cmux.app/Contents/MacOS/cmux", parent: 1},
		{pid: 5001, comm: "zsh", parent: 1623},
		{pid: 5002, comm: "claude", parent: 5001},
	}
	sig := &signal.CmuxSignaler{RunCmd: fakePsRun(procs)}
	server, ok := sig.FindCmuxServerAncestor(5002)
	if !ok {
		t.Fatal("FindCmuxServerAncestor returned !ok for a session under a path-prefixed cmux comm")
	}
	if server != 1623 {
		t.Errorf("FindCmuxServerAncestor = %d, want 1623", server)
	}
}
