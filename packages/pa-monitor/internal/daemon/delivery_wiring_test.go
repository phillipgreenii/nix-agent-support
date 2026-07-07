package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// treeWithOneSession builds a minimal aggregate.Tree containing a single
// Idle session, so Dispatcher's sid->PID lookup (indexSessions) resolves the
// queued intent to pid.
func treeWithOneSession(sid string, pid int) *aggregate.Tree {
	return &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path: "/p",
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: sid,
							PID:       pid,
							Status:    session.Idle,
						},
					},
				},
			},
		},
	}
}

// newTestWatermarks builds a throwaway *WatermarkStore backed by a temp
// runtime.json, standing in for the Recorder/WatermarkView nudger.New needs.
func newTestWatermarks(t *testing.T) *WatermarkStore {
	t.Helper()
	wm, err := NewWatermarkStore(filepath.Join(t.TempDir(), "runtime.json"), nil)
	if err != nil {
		t.Fatalf("NewWatermarkStore: %v", err)
	}
	return wm
}

// TestNudgerWiring_CmuxTargetRoutesToBridgeNotSignaler is the daemon-level
// proof for Task 8's cmux route: with a Nudger built the same way RunWith
// builds it — nudger.New(deliverer, ...) where deliverer is a
// compositeDeliverer over a real bridgeDeliverer (backed by a real
// bridge.Registry + tracker) and an inDaemonDeliverer (backed by the
// tmux/ghostty/vscode signal layer) — dispatching a nudge for a session
// whose PID resolves to a cmux server ancestor pushes a Deliver to that
// server's live bridge stream, and the in-daemon Signaler is never invoked.
// That Signaler stand-in is exactly the layer whose real-world counterpart
// (signal.CmuxSignaler.Send) used to shell out to the `cmux` binary before
// Task 8 rerouted cmux delivery through the bridge — so "Signaler never
// called" is this test's proof that no cmux subprocess is invoked by the
// daemon for this delivery.
func TestNudgerWiring_CmuxTargetRoutesToBridgeNotSignaler(t *testing.T) {
	const (
		sid       = "sid-cmux"
		targetPID = 555
		serverPID = 4242
		bridgePID = 99
	)

	reg := bridge.NewRegistry(time.Minute)
	tr := newTracker()

	var recorded *pb.Deliver
	reg.AttachStream(serverPID, bridgePID, func(m *pb.DaemonMsg) error {
		recorded = m.GetDeliver()
		// Ack immediately, exactly as the BridgeChannel handler's
		// onDeliverResult hook (wired to tr.resolve in RunWith) does when a
		// real bridge reports a DeliverResult.
		go tr.resolve(m.GetDeliver().GetId(), true, "")
		return nil
	})

	ancestor := func(pid int) (int, bool) {
		if pid == targetPID {
			return serverPID, true
		}
		return 0, false
	}
	bridgeDel := &bridgeDeliverer{reg: reg, ancestor: ancestor, tr: tr, timeout: time.Second}
	sig := &fakeSignaler{}
	inDaemonDel := &inDaemonDeliverer{sig: sig}
	deliverer := &compositeDeliverer{ancestor: ancestor, bridge: bridgeDel, inDaemon: inDaemonDel}

	wm := newTestWatermarks(t)
	n := nudger.New(deliverer, wm, nil, nil)

	now := time.Now()
	n.QueueManual([]string{sid}, "wake up cmux", now)
	n.Dispatch(context.Background(), nudger.TickContext{
		Now:        now,
		Tree:       treeWithOneSession(sid, targetPID),
		Watermarks: wm,
	})

	if recorded == nil {
		t.Fatal("expected a Deliver message to have been pushed to the bridge, got none")
	}
	if recorded.GetTargetPid() != targetPID {
		t.Errorf("recorded Deliver.TargetPid = %d, want %d", recorded.GetTargetPid(), targetPID)
	}
	if recorded.GetText() != "wake up cmux" {
		t.Errorf("recorded Deliver.Text = %q, want %q", recorded.GetText(), "wake up cmux")
	}
	if sig.pid != 0 || sig.text != "" {
		t.Errorf("in-daemon Signaler.Send was called with (%d, %q); want never called (cmux target must route via the bridge only)", sig.pid, sig.text)
	}
	if n.PendingFor(sid) {
		t.Error("intent still pending after a successfully-acked bridge delivery")
	}
}

// TestNudgerWiring_NonCmuxTargetRoutesToInDaemonSignaler is the regression
// counterpart: a session whose PID has no cmux server ancestor must still be
// dispatched through the existing in-daemon Signaler path (tmux/ghostty/
// vscode), and must never reach the bridge deliverer.
func TestNudgerWiring_NonCmuxTargetRoutesToInDaemonSignaler(t *testing.T) {
	const (
		sid       = "sid-plain"
		targetPID = 777
	)

	ancestor := func(pid int) (int, bool) { return 0, false }
	bridgeD := &fakeDeliverer{}
	sig := &fakeSignaler{}
	inDaemonDel := &inDaemonDeliverer{sig: sig}
	deliverer := &compositeDeliverer{ancestor: ancestor, bridge: bridgeD, inDaemon: inDaemonDel}

	wm := newTestWatermarks(t)
	n := nudger.New(deliverer, wm, nil, nil)

	now := time.Now()
	n.QueueManual([]string{sid}, "wake up plain", now)
	n.Dispatch(context.Background(), nudger.TickContext{
		Now:        now,
		Tree:       treeWithOneSession(sid, targetPID),
		Watermarks: wm,
	})

	if sig.pid != targetPID || sig.text != "wake up plain" {
		t.Errorf("in-daemon Signaler.Send got (%d, %q), want (%d, %q)", sig.pid, sig.text, targetPID, "wake up plain")
	}
	if bridgeD.calledPID != 0 {
		t.Errorf("bridge deliverer should not have been called, got pid %d", bridgeD.calledPID)
	}
	if n.PendingFor(sid) {
		t.Error("intent still pending after a successful in-daemon delivery")
	}
}
