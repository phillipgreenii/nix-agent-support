package poller

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

func cmuxHost(ws string) *session.Host {
	return &session.Host{Kind: "cmux", CmuxWorkspaceID: ws}
}

func TestRefineCmuxTerminalHost_NilRegistry_ReturnsBareCmux(t *testing.T) {
	if got := refineCmuxTerminalHost(nil, cmuxHost("ws-a")); got != "cmux" {
		t.Errorf("nil registry: got %q, want \"cmux\"", got)
	}
}

func TestRefineCmuxTerminalHost_NoWorkspaceID_ReturnsBareCmux(t *testing.T) {
	br := bridge.NewRegistry(30 * time.Second)
	if got := refineCmuxTerminalHost(br, nil); got != "cmux" {
		t.Errorf("nil host: got %q, want \"cmux\"", got)
	}
	if got := refineCmuxTerminalHost(br, cmuxHost("")); got != "cmux" {
		t.Errorf("empty workspace id: got %q, want \"cmux\"", got)
	}
}

func TestRefineCmuxTerminalHost_NoBridgeRegistered_ReturnsCmuxNoBridge(t *testing.T) {
	br := bridge.NewRegistry(30 * time.Second)
	if got := refineCmuxTerminalHost(br, cmuxHost("ws-a")); got != "cmux (no bridge)" {
		t.Errorf("no bridge: got %q, want \"cmux (no bridge)\"", got)
	}
}

func TestRefineCmuxTerminalHost_RegisteredAndAlive_ReturnsCmux(t *testing.T) {
	br := bridge.NewRegistry(30 * time.Second)
	br.AttachStream("ws-a", 5000, 5001)
	if got := refineCmuxTerminalHost(br, cmuxHost("ws-a")); got != "cmux" {
		t.Errorf("alive bridge: got %q, want \"cmux\"", got)
	}
}

func TestRefineCmuxTerminalHost_RegisteredButStale_ReturnsBridgeDisconnected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	br := bridge.NewRegistry(30 * time.Second)
	br.SetNowForTest(func() time.Time { return now })
	br.AttachStream("ws-a", 5000, 5001)
	br.SetNowForTest(func() time.Time { return now.Add(31 * time.Second) })
	if got := refineCmuxTerminalHost(br, cmuxHost("ws-a")); got != "cmux (bridge disconnected)" {
		t.Errorf("stale bridge: got %q, want \"cmux (bridge disconnected)\"", got)
	}
}

// A bridge registered for another workspace does not satisfy this session.
func TestRefineCmuxTerminalHost_OtherWorkspaceBridge_StillNoBridge(t *testing.T) {
	br := bridge.NewRegistry(30 * time.Second)
	br.AttachStream("ws-b", 5000, 5001)
	if got := refineCmuxTerminalHost(br, cmuxHost("ws-a")); got != "cmux (no bridge)" {
		t.Errorf("other workspace bridge: got %q, want \"cmux (no bridge)\"", got)
	}
}
