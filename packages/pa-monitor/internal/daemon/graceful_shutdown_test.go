package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// TestRun_ShutsDownPromptlyWithOpenWatchStream asserts that a daemon SIGTERM
// (modelled here by cancelling Run's context) completes promptly even while a
// client holds an open WatchState stream. GracefulStop waits for in-flight RPC
// handlers to return but does NOT cancel their stream contexts, and WatchState
// only breaks on stream.Context().Done() — so without a server-shutdown signal
// the handler blocks and GracefulStop hangs until the client disconnects,
// forcing a restart to rely on launchd's SIGKILL-after-timeout (bead pg2-fcjpr).
//
// The 2s bound is well under launchd's ExitTimeOut and below the graceful-stop
// hard-fallback budget, so a pass proves the shutdown SIGNAL unblocked the
// handler rather than the hard-stop fallback.
func TestRun_ShutsDownPromptlyWithOpenWatchStream(t *testing.T) {
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
	defer func() { _ = conn.Close() }()

	client := pb.NewPaMonitorClient(conn)
	stream, err := client.WatchState(context.Background(), &pb.WatchStateRequest{})
	if err != nil {
		t.Fatalf("WatchState: %v", err)
	}
	// Establish the stream: the handler is now running server-side, holding a
	// long-lived stream whose context GracefulStop does not cancel.
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("recv first state: %v", err)
	}

	// Trigger shutdown while the stream is still open.
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s with a WatchState stream held open: " +
			"GracefulStop is blocking on the live stream (bead pg2-fcjpr)")
	}
}

// TestBridgeChannel_ReturnsOnServerShutdown asserts the BridgeChannel handler
// returns when the server-shutdown signal fires, even though its stream context
// is never cancelled (mimicking an always-connected cmux bridge during a
// graceful daemon stop). Before the fix the handler only broke on ctx.Done(),
// so a graceful stop blocked until the bridge disconnected (bead pg2-fcjpr).
func TestBridgeChannel_ReturnsOnServerShutdown(t *testing.T) {
	srv := newBridgeTestServer(t)
	shutdown := make(chan struct{})
	srv.shutdown = shutdown

	// A live stream whose context is never cancelled for the duration of the
	// shutdown assertion.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeBridgeStream(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.BridgeChannel(fake) }()

	// Signal server shutdown; the handler must return promptly (teardown runs).
	close(shutdown)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("BridgeChannel returned %v on shutdown, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeChannel did not return within 2s after the server shutdown signal (bead pg2-fcjpr)")
	}
}
