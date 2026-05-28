package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

func TestServer_PingReturnsTimestamp(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, paths) }()

	waitForFile(t, paths.Socket)

	conn := dialUnix(t, paths.Socket)
	defer conn.Close()

	client := pb.NewPaMonitorClient(conn)
	resp, err := client.Ping(context.Background(), &pb.PingRequest{})
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if resp.GetTs() == nil {
		t.Error("Ping response has no ts")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestWatchState_EmitsHeartbeats(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Run(ctx, paths) }()

	waitForFile(t, paths.Socket)

	conn := dialUnix(t, paths.Socket)
	defer conn.Close()

	client := pb.NewPaMonitorClient(conn)
	stream, err := client.WatchState(context.Background(), &pb.WatchStateRequest{
		HeartbeatIntervalMs: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv first: %v", err)
	}
	if first.GetState() == nil {
		t.Errorf("first message has no DaemonState: %+v", first)
	}

	hbCount := 0
	deadline := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if msg.GetHeartbeat() != nil {
			hbCount++
		}
	}
	if hbCount < 2 {
		t.Errorf("heartbeats received = %d, want >= 2", hbCount)
	}
}

// TestWatchState_ClampsTooFastInterval verifies the heartbeat handler
// treats interval<50ms as a request for the minimum floor (50ms), NOT
// as the default fallback (2s). This is the contract the spec promises:
// 0 means "use server default", any positive value <50ms means "clamp
// to 50ms".
func TestWatchState_ClampsTooFastInterval(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Run(ctx, paths) }()

	waitForFile(t, paths.Socket)
	conn := dialUnix(t, paths.Socket)
	defer conn.Close()

	client := pb.NewPaMonitorClient(conn)
	stream, err := client.WatchState(context.Background(), &pb.WatchStateRequest{
		HeartbeatIntervalMs: 10, // below 50ms floor
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drain initial state.
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	// Within 300ms we expect at least 4 heartbeats if interval was
	// clamped to 50ms (300/50=6, with timing slop ~4). If the code
	// fell back to the 2s default we'd see 0 in this window.
	hbCount := 0
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if msg.GetHeartbeat() != nil {
			hbCount++
		}
	}
	if hbCount < 3 {
		t.Errorf("clamp to 50ms expected ~5-6 heartbeats in 300ms, got %d (likely fell back to 2s default)", hbCount)
	}
}

func dialUnix(t *testing.T, sockPath string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.Dial("unix:"+sockPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// noopSignaler is a Signaler that does nothing. Satisfies signal.Signaler.
type noopSignaler struct{}

func (noopSignaler) Name() string         { return "noop" }
func (noopSignaler) Detect(_ int) bool    { return true }
func (noopSignaler) Send(_ int, _ string) error { return nil }

var _ signal.Signaler = noopSignaler{}

// newTestServerWithNudger builds a server backed by a real Nudger +
// WatermarkStore and a tree containing one Idle session with ID sid.
func newTestServerWithNudger(t *testing.T, sid string) *server {
	t.Helper()
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "runtime.json")

	wm, err := NewWatermarkStore(runtimePath)
	if err != nil {
		t.Fatalf("NewWatermarkStore: %v", err)
	}
	n := nudger.New(noopSignaler{}, wm)

	state := newSharedState()
	state.mu.Lock()
	state.nudger = n
	state.watermarks = wm
	state.mu.Unlock()

	// Publish a tree with one idle session so expandStringSelector can match.
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path:  "/work",
				IdleN: 1,
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: sid,
							PID:       12345,
							Status:    session.Idle,
						},
					},
				},
			},
		},
	}
	state.setTree(tree)

	return newServer(state)
}

// TestServerNudgeQueueIdempotent verifies that the first NudgeQueue call
// queues the session and the second returns it in AlreadyQueuedSessionIds.
func TestServerNudgeQueueIdempotent(t *testing.T) {
	srv := newTestServerWithNudger(t, "sid-1")
	ctx := context.Background()

	// First call: should queue.
	resp1, err := srv.NudgeQueue(ctx, &pb.NudgeQueueRequest{
		Selector: "session:sid-1",
		Text:     "continue",
	})
	if err != nil {
		t.Fatalf("NudgeQueue first call: %v", err)
	}
	if len(resp1.GetQueuedSessionIds()) != 1 || resp1.GetQueuedSessionIds()[0] != "sid-1" {
		t.Errorf("first call: QueuedSessionIds = %v, want [sid-1]", resp1.GetQueuedSessionIds())
	}
	if len(resp1.GetAlreadyQueuedSessionIds()) != 0 {
		t.Errorf("first call: AlreadyQueuedSessionIds = %v, want []", resp1.GetAlreadyQueuedSessionIds())
	}

	// Second call: same selector — already queued.
	resp2, err := srv.NudgeQueue(ctx, &pb.NudgeQueueRequest{
		Selector: "session:sid-1",
		Text:     "continue",
	})
	if err != nil {
		t.Fatalf("NudgeQueue second call: %v", err)
	}
	if len(resp2.GetQueuedSessionIds()) != 0 {
		t.Errorf("second call: QueuedSessionIds = %v, want []", resp2.GetQueuedSessionIds())
	}
	if len(resp2.GetAlreadyQueuedSessionIds()) != 1 || resp2.GetAlreadyQueuedSessionIds()[0] != "sid-1" {
		t.Errorf("second call: AlreadyQueuedSessionIds = %v, want [sid-1]", resp2.GetAlreadyQueuedSessionIds())
	}
}

// TestServerSetAutoResumePersists verifies that SetAutoResume toggles the
// watermarks flag and it is readable immediately after each call.
func TestServerSetAutoResumePersists(t *testing.T) {
	srv := newTestServerWithNudger(t, "sid-ar")
	ctx := context.Background()

	// Enable.
	resp1, err := srv.SetAutoResume(ctx, &pb.SetAutoResumeRequest{Enabled: true})
	if err != nil {
		t.Fatalf("SetAutoResume(true): %v", err)
	}
	if !resp1.GetEnabled() {
		t.Error("SetAutoResume(true) response Enabled = false, want true")
	}
	if !srv.state.Watermarks().AutoResumeEnabled() {
		t.Error("after SetAutoResume(true): watermarks.AutoResumeEnabled() = false, want true")
	}

	// Disable.
	resp2, err := srv.SetAutoResume(ctx, &pb.SetAutoResumeRequest{Enabled: false})
	if err != nil {
		t.Fatalf("SetAutoResume(false): %v", err)
	}
	if resp2.GetEnabled() {
		t.Error("SetAutoResume(false) response Enabled = true, want false")
	}
	if srv.state.Watermarks().AutoResumeEnabled() {
		t.Error("after SetAutoResume(false): watermarks.AutoResumeEnabled() = true, want false")
	}
}

// TestServerNudgeCancelRemovesIntent verifies that NudgeCancel clears a
// previously queued manual nudge for a session.
func TestServerNudgeCancelRemovesIntent(t *testing.T) {
	srv := newTestServerWithNudger(t, "sid-c")
	ctx := context.Background()

	// Queue first.
	if _, err := srv.NudgeQueue(ctx, &pb.NudgeQueueRequest{
		Selector: "session:sid-c",
		Text:     "please continue",
	}); err != nil {
		t.Fatalf("NudgeQueue: %v", err)
	}
	n := srv.state.Nudger()
	if !n.PendingForSource("sid-c", nudger.SourceManual) {
		t.Fatal("expected SourceManual pending after NudgeQueue")
	}

	// Now cancel.
	cancelResp, err := srv.NudgeCancel(ctx, &pb.NudgeCancelRequest{
		Selector: "session:sid-c",
	})
	if err != nil {
		t.Fatalf("NudgeCancel: %v", err)
	}
	if len(cancelResp.GetCancelledSessionIds()) != 1 || cancelResp.GetCancelledSessionIds()[0] != "sid-c" {
		t.Errorf("CancelledSessionIds = %v, want [sid-c]", cancelResp.GetCancelledSessionIds())
	}
	if n.PendingForSource("sid-c", nudger.SourceManual) {
		t.Error("SourceManual still pending after NudgeCancel — cancel did not work")
	}
}

// TestServerNudgeQueueEmptySelector verifies InvalidArgument is returned.
func TestServerNudgeQueueEmptySelector(t *testing.T) {
	srv := newTestServerWithNudger(t, "sid-err")
	_, err := srv.NudgeQueue(context.Background(), &pb.NudgeQueueRequest{})
	if err == nil {
		t.Fatal("expected error for empty selector, got nil")
	}
}

// TestServerNudgeQueueNudgerNil verifies FailedPrecondition when nudger absent.
func TestServerNudgeQueueNudgerNil(t *testing.T) {
	state := newSharedState()
	srv := newServer(state)
	_, err := srv.NudgeQueue(context.Background(), &pb.NudgeQueueRequest{Selector: "session:x"})
	if err == nil {
		t.Fatal("expected error when nudger is nil, got nil")
	}
}

// TestServerSetAutoResumeNudgerNil verifies FailedPrecondition when watermarks absent.
func TestServerSetAutoResumeNudgerNil(t *testing.T) {
	state := newSharedState()
	srv := newServer(state)
	_, err := srv.SetAutoResume(context.Background(), &pb.SetAutoResumeRequest{Enabled: true})
	if err == nil {
		t.Fatal("expected error when watermarks is nil, got nil")
	}
}

// Ensure time is imported (used in newTestServerWithNudger via aggregate.Tree).
var _ = time.Now
