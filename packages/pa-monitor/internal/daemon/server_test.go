package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
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

func TestWatchState_PushesPeriodicState(t *testing.T) {
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
		PushIntervalMs: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv first: %v", err)
	}
	if first == nil {
		t.Errorf("first message has no DaemonState: %+v", first)
	}

	// After the initial state, the daemon pushes State events on every
	// tick (was Heartbeats; now State so subscribers see RPC-driven
	// changes like Caffeinate / SetAutoResume immediately).
	stateCount := 0
	deadline := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if msg != nil {
			stateCount++
		}
	}
	if stateCount < 2 {
		t.Errorf("periodic states received = %d, want >= 2", stateCount)
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
		PushIntervalMs: 10, // below 50ms floor
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drain initial state.
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	// Within 300ms we expect at least 4 state pushes if interval was
	// clamped to 50ms (300/50=6, with timing slop ~4). If the code
	// fell back to the 2s default we'd see 0 in this window.
	stateCount := 0
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if msg != nil {
			stateCount++
		}
	}
	if stateCount < 3 {
		t.Errorf("clamp to 50ms expected ~5-6 state pushes in 300ms, got %d (likely fell back to 2s default)", stateCount)
	}
}

func dialUnix(t *testing.T, sockPath string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.Dial(
		"unix:"+sockPath,
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

func (noopSignaler) Name() string               { return "noop" }
func (noopSignaler) Detect(_ int) bool          { return true }
func (noopSignaler) Send(_ int, _ string) error { return nil }

var _ signal.Signaler = noopSignaler{}

// newTestServerWithNudger builds a server backed by a real Nudger +
// WatermarkStore and an in-memory SQLite DB containing one Idle session
// with ID sid. The ReadService is wired so snapshot() reads from the DB.
func newTestServerWithNudger(t *testing.T, sid string) *server {
	t.Helper()
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "runtime.json")

	// --- in-memory SQLite + WriteService + ReadService ---
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	deps := service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	}
	ws := service.NewWriteService(deps)
	ws.Start(context.Background())
	t.Cleanup(ws.Stop)

	// Seed one Idle session so expandStringSelector can match.
	pid := 12345
	now := time.Now()
	if err := ws.UpsertSession(context.Background(), store.Session{
		SessionID:       sid,
		PID:             &pid,
		Cwd:             "/work",
		Status:          "idle",
		LastProcessedAt: now,
		UpdatedAt:       now,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	// Sync so the row is visible before any snapshot() call.
	if err := ws.Sync(context.Background()); err != nil {
		t.Fatalf("ws.Sync: %v", err)
	}

	rs := service.NewReadService(service.ReadDeps{
		Sessions: sqlite.NewSessionStore(db),
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})

	wm, err := NewWatermarkStore(runtimePath, nil)
	if err != nil {
		t.Fatalf("NewWatermarkStore: %v", err)
	}
	n := nudger.New(noopSignaler{}, wm, nil, nil)

	state := newSharedState()
	state.mu.Lock()
	state.nudger = n
	state.watermarks = wm
	state.mu.Unlock()
	state.setReadService(rs)

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

// TestServerNudgeQueueUsesConfiguredDefault verifies that NudgeQueue calls
// with an empty Text fall back to the server's configured autoResumeMessage,
// and that the literal "continue" sentinel is only used when the
// configured default is also empty.
func TestServerNudgeQueueUsesConfiguredDefault(t *testing.T) {
	srv := newTestServerWithNudger(t, "sid-defaulted")
	srv.autoResumeMessage = "carry on"
	ctx := context.Background()

	if _, err := srv.NudgeQueue(ctx, &pb.NudgeQueueRequest{
		Selector: "session:sid-defaulted",
	}); err != nil {
		t.Fatalf("NudgeQueue: %v", err)
	}
	intents := srv.state.Nudger().SnapshotStore()
	if len(intents) != 1 {
		t.Fatalf("expected 1 queued intent, got %d", len(intents))
	}
	if intents[0].Text != "carry on" {
		t.Errorf("queued text = %q, want %q (configured autoResumeMessage)", intents[0].Text, "carry on")
	}
}

func TestServerNudgeQueueFallsBackToContinue(t *testing.T) {
	srv := newTestServerWithNudger(t, "sid-fallback")
	// autoResumeMessage left empty — should drop through to the "continue" sentinel.
	ctx := context.Background()

	if _, err := srv.NudgeQueue(ctx, &pb.NudgeQueueRequest{
		Selector: "session:sid-fallback",
	}); err != nil {
		t.Fatalf("NudgeQueue: %v", err)
	}
	intents := srv.state.Nudger().SnapshotStore()
	if len(intents) != 1 {
		t.Fatalf("expected 1 queued intent, got %d", len(intents))
	}
	if intents[0].Text != "continue" {
		t.Errorf("queued text = %q, want %q (final fallback)", intents[0].Text, "continue")
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

func TestServerCaffeinatePersistsToToggleStore(t *testing.T) {
	// Setup: in-memory DB + WriteService + new test server.
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ws := service.NewWriteService(service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ws.Start(context.Background())
	t.Cleanup(ws.Stop)

	state := newSharedState()
	srv := newServer(state)
	srv.writeService = ws

	// Call Caffeinate({Action: "on"}).
	ctx := context.Background()
	resp, err := srv.Caffeinate(ctx, &pb.CaffeinateRequest{Action: "on"})
	if err != nil {
		t.Fatalf("Caffeinate: %v", err)
	}
	if !resp.GetActive() {
		t.Error("Caffeinate response Active = false, want true")
	}

	// Sync WriteService so DB writes complete.
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("ws.Sync: %v", err)
	}

	// Read ToggleStore directly to verify persistence.
	ts := sqlite.NewToggleStore(db)
	val, present, err := ts.Get(ctx, "caffeinate_on")
	if err != nil {
		t.Fatalf("ToggleStore.Get(caffeinate_on): %v", err)
	}
	if !present {
		t.Error("caffeinate_on not present in ToggleStore after Caffeinate(on)")
	}
	if !val {
		t.Error("caffeinate_on = false in ToggleStore, want true")
	}
}

func TestServerSetAutoResumePersistsToToggleStore(t *testing.T) {
	// Setup: in-memory DB + WriteService + new test server.
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ws := service.NewWriteService(service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ws.Start(context.Background())
	t.Cleanup(ws.Stop)

	state := newSharedState()
	srv := newServer(state)
	srv.writeService = ws

	// Create a watermark store so SetAutoResume doesn't fail.
	dir := t.TempDir()
	wm, err := NewWatermarkStore(filepath.Join(dir, "runtime.json"), nil)
	if err != nil {
		t.Fatalf("NewWatermarkStore: %v", err)
	}
	state.mu.Lock()
	state.watermarks = wm
	state.mu.Unlock()

	// Call SetAutoResume({Enabled: true}).
	ctx := context.Background()
	resp, err := srv.SetAutoResume(ctx, &pb.SetAutoResumeRequest{Enabled: true})
	if err != nil {
		t.Fatalf("SetAutoResume: %v", err)
	}
	if !resp.GetEnabled() {
		t.Error("SetAutoResume response Enabled = false, want true")
	}

	// Sync WriteService so DB writes complete.
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("ws.Sync: %v", err)
	}

	// Read ToggleStore directly to verify persistence.
	ts := sqlite.NewToggleStore(db)
	val, present, err := ts.Get(ctx, "auto_resume_enabled")
	if err != nil {
		t.Fatalf("ToggleStore.Get(auto_resume_enabled): %v", err)
	}
	if !present {
		t.Error("auto_resume_enabled not present in ToggleStore after SetAutoResume(true)")
	}
	if !val {
		t.Error("auto_resume_enabled = false in ToggleStore, want true")
	}
}
