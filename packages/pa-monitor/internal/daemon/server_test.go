package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
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
