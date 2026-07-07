package daemon

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"

	"google.golang.org/grpc"
)

// fakeBridgeStream implements pb.PaMonitor_BridgeChannelServer
// (grpc.BidiStreamingServer[BridgeMsg, DaemonMsg]) over channels so the
// BridgeChannel handler can be driven without a real transport.
//
//   - Recv blocks on an inbound channel, returning io.EOF when the channel is
//     closed and ctx.Err() when the stream context is cancelled (matching real
//     gRPC's ctx-interruptible Recv).
//   - Send appends to a mutex-guarded slice so assertions are race-free.
//   - Context returns the test-controlled cancelable context.
//
// The embedded grpc.ServerStream (nil) supplies the SetHeader/SendHeader/
// SetTrailer/SendMsg/RecvMsg methods needed to satisfy the interface; the
// handler never calls them.
type fakeBridgeStream struct {
	grpc.ServerStream
	ctx    context.Context
	recvCh chan *pb.BridgeMsg

	mu   sync.Mutex
	sent []*pb.DaemonMsg
}

func newFakeBridgeStream(ctx context.Context) *fakeBridgeStream {
	return &fakeBridgeStream{ctx: ctx, recvCh: make(chan *pb.BridgeMsg, 8)}
}

func (f *fakeBridgeStream) Recv() (*pb.BridgeMsg, error) {
	select {
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	case m, ok := <-f.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	}
}

func (f *fakeBridgeStream) Send(m *pb.DaemonMsg) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	return nil
}

func (f *fakeBridgeStream) Context() context.Context { return f.ctx }

func (f *fakeBridgeStream) sentSnapshotCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.sent {
		if m.GetSnapshot() != nil {
			n++
		}
	}
	return n
}

func (f *fakeBridgeStream) sawDeliver(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.sent {
		if d := m.GetDeliver(); d != nil && d.GetId() == id {
			return true
		}
	}
	return false
}

// waitFor polls cond until it is true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func newBridgeTestServer(t *testing.T) *server {
	t.Helper()
	srv := newServer(newSharedState())
	srv.bridges = bridge.NewRegistry(30 * time.Second)
	// Fast ticker so the periodic snapshot path is genuinely exercised
	// without a multi-second wait.
	srv.bridgeSnapshotInterval = 20 * time.Millisecond
	return srv
}

// TestBridgeChannelRegisterPushesSnapshotAndDelivers covers the core happy
// path: Register attaches a live bridge, snapshots are pushed to the client,
// and a Deliver enqueued through the registry entry's send hook reaches the
// client's Send side. Cancelling the context tears the stream down and
// deregisters the bridge.
func TestBridgeChannelRegisterPushesSnapshotAndDelivers(t *testing.T) {
	srv := newBridgeTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeBridgeStream(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.BridgeChannel(fake) }()

	const serverPID, bridgePID = 4321, 5678
	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Register{Register: &pb.Register{
		BridgePid:   bridgePID,
		ServerPid:   serverPID,
		WorkspaceId: "ws-1",
	}}}

	// (a) registry gains a live bridge.
	var entry *bridge.BridgeEntry
	if !waitFor(t, 2*time.Second, func() bool {
		e, ok := srv.bridges.LiveBridge(serverPID)
		if ok {
			entry = e
		}
		return ok
	}) {
		t.Fatalf("LiveBridge(%d) never became live", serverPID)
	}

	// (b) a snapshot reaches the client's Send side (immediate-on-register
	// plus the periodic ticker).
	if !waitFor(t, 2*time.Second, func() bool { return fake.sentSnapshotCount() >= 1 }) {
		t.Fatalf("no snapshot reached Send")
	}
	// Periodic ticker keeps producing snapshots.
	if !waitFor(t, 2*time.Second, func() bool { return fake.sentSnapshotCount() >= 2 }) {
		t.Fatalf("snapshot ticker did not keep pushing (got %d)", fake.sentSnapshotCount())
	}

	// (c) a Deliver pushed via the entry's send hook reaches Send.
	if err := entry.Send(&pb.DaemonMsg{Kind: &pb.DaemonMsg_Deliver{Deliver: &pb.Deliver{
		Id:        "cmd-1",
		TargetPid: 9999,
		Text:      "continue",
	}}}); err != nil {
		t.Fatalf("entry.Send(deliver): %v", err)
	}
	if !waitFor(t, 2*time.Second, func() bool { return fake.sawDeliver("cmd-1") }) {
		t.Fatalf("Deliver did not reach Send")
	}

	// Teardown via ctx cancel deregisters the bridge and returns.
	cancel()
	if !waitFor(t, 2*time.Second, func() bool {
		_, ok := srv.bridges.LiveBridge(serverPID)
		return !ok
	}) {
		t.Fatalf("bridge not deregistered after ctx cancel")
	}
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("BridgeChannel did not return after ctx cancel")
	}
}

// TestBridgeChannelHeartbeatRefreshesMember verifies a Heartbeat keeps the
// registered bridge live (it is dispatched to Registry.Heartbeat under the
// stream's registered server PID).
func TestBridgeChannelHeartbeatRefreshesMember(t *testing.T) {
	srv := newBridgeTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeBridgeStream(ctx)
	go func() { _ = srv.BridgeChannel(fake) }()

	const serverPID, bridgePID = 8100, 8101
	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Register{Register: &pb.Register{
		BridgePid: bridgePID, ServerPid: serverPID, WorkspaceId: "ws-hb",
	}}}
	if !waitFor(t, 2*time.Second, func() bool {
		_, ok := srv.bridges.LiveBridge(serverPID)
		return ok
	}) {
		t.Fatalf("bridge never became live")
	}
	// A Heartbeat should be accepted without error and keep the member alive.
	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Heartbeat{Heartbeat: &pb.Heartbeat{
		BridgePid: bridgePID,
	}}}
	// Give the reader a moment to process, then confirm still live.
	time.Sleep(50 * time.Millisecond)
	if _, ok := srv.bridges.LiveBridge(serverPID); !ok {
		t.Fatalf("bridge not live after heartbeat")
	}
}

// TestBridgeChannelDeliverResultInvokesCallback verifies a DeliverResult from
// the bridge is routed to the server's onDeliverResult callback.
func TestBridgeChannelDeliverResultInvokesCallback(t *testing.T) {
	srv := newBridgeTestServer(t)

	var mu sync.Mutex
	var (
		gotID, gotErr string
		gotOK, called bool
	)
	srv.onDeliverResult = func(id string, ok bool, errStr string) {
		mu.Lock()
		gotID, gotOK, gotErr, called = id, ok, errStr, true
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeBridgeStream(ctx)
	go func() { _ = srv.BridgeChannel(fake) }()

	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Result{Result: &pb.DeliverResult{
		Id:    "cmd-9",
		Ok:    false,
		Error: "boom",
	}}}

	if !waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return called
	}) {
		t.Fatalf("onDeliverResult never fired")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotID != "cmd-9" || gotOK != false || gotErr != "boom" {
		t.Fatalf("onDeliverResult got (%q,%v,%q), want (cmd-9,false,boom)", gotID, gotOK, gotErr)
	}
}

// TestBridgeChannelCtxCancelNotifiesStreamClosed verifies teardown on context
// cancellation: the bridge is deregistered and onStreamClosed fires with the
// stream's server PID.
func TestBridgeChannelCtxCancelNotifiesStreamClosed(t *testing.T) {
	srv := newBridgeTestServer(t)

	var mu sync.Mutex
	var (
		closedPID    int
		closedCalled bool
	)
	srv.onStreamClosed = func(serverPID int) {
		mu.Lock()
		closedPID, closedCalled = serverPID, true
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	fake := newFakeBridgeStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.BridgeChannel(fake) }()

	const serverPID = 7000
	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Register{Register: &pb.Register{
		BridgePid: 7001, ServerPid: serverPID, WorkspaceId: "ws-x",
	}}}
	if !waitFor(t, 2*time.Second, func() bool {
		_, ok := srv.bridges.LiveBridge(serverPID)
		return ok
	}) {
		t.Fatalf("bridge never became live")
	}

	cancel()

	if !waitFor(t, 2*time.Second, func() bool {
		_, ok := srv.bridges.LiveBridge(serverPID)
		return !ok
	}) {
		t.Fatalf("bridge not deregistered after ctx cancel")
	}
	if !waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return closedCalled
	}) {
		t.Fatalf("onStreamClosed never fired")
	}
	mu.Lock()
	pid := closedPID
	mu.Unlock()
	if pid != serverPID {
		t.Fatalf("onStreamClosed(serverPID) = %d, want %d", pid, serverPID)
	}
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("BridgeChannel did not return after ctx cancel")
	}
}

// TestBridgeChannelSkipsInvalidRegister verifies a Register with a
// non-positive server PID is ignored (no live bridge attached).
func TestBridgeChannelSkipsInvalidRegister(t *testing.T) {
	srv := newBridgeTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeBridgeStream(ctx)
	go func() { _ = srv.BridgeChannel(fake) }()

	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Register{Register: &pb.Register{
		BridgePid: 1234, ServerPid: 0, WorkspaceId: "ws-bad",
	}}}
	// Nothing should ever register under any server PID; sample the invalid
	// key after a short settle.
	time.Sleep(80 * time.Millisecond)
	if _, ok := srv.bridges.LiveBridge(0); ok {
		t.Fatalf("invalid Register (server_pid=0) attached a live bridge")
	}
}
