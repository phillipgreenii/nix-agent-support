package poller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// fakePsRun mirrors the helper in internal/signal/cmux_detect_test.go. The
// poller package cannot import the signal package's test helpers (separate
// package), so we duplicate just enough to drive a real CmuxSignaler through
// its ps-based ancestry path.
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
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		if len(args) >= 3 && args[0] == "-A" && args[1] == "-o" && args[2] == "pid,comm" {
			var sb strings.Builder
			sb.WriteString("  PID COMMAND\n")
			for _, p := range procs {
				sb.WriteString(fmt.Sprintf("%5d %s\n", p.pid, p.comm))
			}
			return []byte(sb.String()), nil
		}
		if len(args) == 4 && args[0] == "-o" && args[1] == "ppid=" && args[2] == "-p" {
			var pid int
			if _, err := fmt.Sscanf(args[3], "%d", &pid); err != nil {
				return nil, fmt.Errorf("bad pid: %q", args[3])
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

// cmuxSignalerWith returns a CmuxSignaler stub wired to a fake ps. The
// signalers slice is what refineCmuxTerminalHost expects.
func cmuxSignalerWith(procs []fakeProc) []signal.Signaler {
	cs := &signal.CmuxSignaler{RunCmd: fakePsRun(procs)}
	return []signal.Signaler{cs}
}

// procsForCmuxSession returns a minimal process tree:
//
//	cmuxServer (5000) <- shell (5001) <- session (5002)
//
// Reused across the table tests below.
func procsForCmuxSession() []fakeProc {
	return []fakeProc{
		{pid: 5000, comm: "cmux", parent: 1},
		{pid: 5001, comm: "zsh", parent: 5000},
		{pid: 5002, comm: "claude", parent: 5001},
	}
}

func TestRefineCmuxTerminalHost_NilRegistry_ReturnsBareCmux(t *testing.T) {
	sigs := cmuxSignalerWith(procsForCmuxSession())
	got := refineCmuxTerminalHost(sigs, nil, 5002)
	if got != "cmux" {
		t.Errorf("nil registry: got %q, want \"cmux\"", got)
	}
}

func TestRefineCmuxTerminalHost_NoBridgeRegistered_ReturnsCmuxNoBridge(t *testing.T) {
	br := bridge.NewRegistry(30 * time.Second)
	sigs := cmuxSignalerWith(procsForCmuxSession())
	got := refineCmuxTerminalHost(sigs, br, 5002)
	if got != "cmux (no bridge)" {
		t.Errorf("no bridge: got %q, want \"cmux (no bridge)\"", got)
	}
}

func TestRefineCmuxTerminalHost_RegisteredAndAlive_ReturnsCmux(t *testing.T) {
	br := bridge.NewRegistry(30 * time.Second)
	br.AttachStream(5000 /*server pid*/, 5001 /*bridge pid*/, nil)
	sigs := cmuxSignalerWith(procsForCmuxSession())
	got := refineCmuxTerminalHost(sigs, br, 5002)
	if got != "cmux" {
		t.Errorf("alive bridge: got %q, want \"cmux\"", got)
	}
}

func TestRefineCmuxTerminalHost_RegisteredButStale_ReturnsBridgeDisconnected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	br := bridge.NewRegistry(30 * time.Second)
	br.SetNowForTest(func() time.Time { return now })
	br.AttachStream(5000, 5001, nil)
	br.SetNowForTest(func() time.Time { return now.Add(31 * time.Second) })

	sigs := cmuxSignalerWith(procsForCmuxSession())
	got := refineCmuxTerminalHost(sigs, br, 5002)
	if got != "cmux (bridge disconnected)" {
		t.Errorf("stale bridge: got %q, want \"cmux (bridge disconnected)\"", got)
	}
}
