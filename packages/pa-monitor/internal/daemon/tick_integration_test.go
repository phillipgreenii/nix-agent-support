//go:build integration

package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/poller"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// TestRunWith_IntegratesPollerTrackersAndState verifies that:
//  1. GetState reflects synthesised sessions persisted via WriteService.
//  2. IsAnyBusy returns true when any directory has working sessions.
//  3. block.Tracker advances to the synthesised block id.
//
// The stubPoller writes sessions directly into the DB (via WriteService)
// on each Snapshot call so the DB-only snapshot() path can read them back.
func TestRunWith_IntegratesPollerTrackersAndState(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	// --- in-memory SQLite + WriteService + ReadService ---
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

	rs := service.NewReadService(service.ReadDeps{
		Sessions: sqlite.NewSessionStore(db),
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})

	// Synthesised state: 2 working sessions, 1 idle, one active block.
	activeBlock := &usage.Block{
		StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC),
		IsActive:  true,
		CostUSD:   100.0, // above cap
	}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path:     "/p1",
				WorkingN: 2,
				IdleN:    1,
				Sessions: []*aggregate.SessionView{
					{Session: &session.Session{SessionID: "a", Status: session.Working, Cwd: "/p1"}},
					{Session: &session.Session{SessionID: "b", Status: session.Working, Cwd: "/p1"}},
					{Session: &session.Session{SessionID: "c", Status: session.Idle, Cwd: "/p1"}},
				},
			},
		},
		ActiveBlock: activeBlock,
	}

	bt := block.NewTracker() // block.id correlator (cost-cap trigger retired, ADR 0024 D3)
	wt := week.NewTracker()

	hitCount := atomic.Int32{}

	// tickReady is closed once the first full tick (write + sync) has landed.
	tickReady := make(chan struct{})
	var tickReadyOnce sync.Once

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths: paths,
			Tick:  50 * time.Millisecond,
			Poller: &stubPoller{
				snapshot: func(ctx context.Context) (*aggregate.Tree, bool, error) {
					// Write sessions into the DB so the DB-path snapshot() can
					// read them back. This mirrors what the real *poller.Poller
					// does via its embedded WriteService.
					now := time.Now()
					pid := 12345
					statuses := map[string]string{"a": "working", "b": "working", "c": "idle"}
					for sid, status := range statuses {
						_ = ws.UpsertSession(ctx, store.Session{
							SessionID:       sid,
							PID:             &pid,
							Cwd:             "/p1",
							Status:          status,
							LastProcessedAt: now,
							UpdatedAt:       now,
							CreatedAt:       now,
						})
					}
					_ = ws.Sync(ctx)
					tickReadyOnce.Do(func() { close(tickReady) })
					return tree, true, nil
				},
			},
			WriteService: ws,
			ReadService:  rs,
			BlockTracker: bt,
			WeekTracker:  wt,
		})
	}()

	waitForFile(t, paths.Socket)

	// Wait for the first tick to land in the DB.
	select {
	case <-tickReady:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first tick")
	}

	// Poll until BOTH the DB-derived GetState and the in-memory IsAnyBusy
	// reflect the synthesised tree. tickReady is signalled mid-tick (inside
	// Snapshot, before the daemon publishes the post-tick in-memory state), and
	// the two RPCs read different sources that update at different points in the
	// tick: GetState/buildState reads the DB (written early, inside Snapshot)
	// while IsAnyBusy reads the in-memory snapshot (published late in the tick).
	// A single read of either can therefore race the publish under -race's
	// slower timing, so wait for both to converge.
	conn := dialUnix(t, paths.Socket)
	defer func() { _ = conn.Close() }()
	client := pb.NewPaMonitorClient(conn)

	var dirCount int
	var workingN uint32
	var anyBusy bool
	var busyCount uint32
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err := client.GetState(context.Background(), &pb.GetStateRequest{})
		if err != nil {
			t.Fatal(err)
		}
		dirs := state.GetDirs()
		dirCount = len(dirs)
		workingN = 0
		if dirCount == 1 {
			workingN = dirs[0].GetWorkingN()
		}

		busy, err := client.IsAnyBusy(context.Background(), &pb.IsAnyBusyRequest{})
		if err != nil {
			t.Fatal(err)
		}
		anyBusy = busy.GetAnyBusy()
		busyCount = busy.GetBusyCount()

		if dirCount == 1 && workingN == 2 && anyBusy && busyCount == 2 {
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dirCount != 1 || workingN != 2 {
		t.Errorf("daemon state did not reflect synthesised tree: dirs=%d workingN=%d", dirCount, workingN)
	}
	if !anyBusy || busyCount != 2 {
		t.Errorf("IsAnyBusy = (any=%v, count=%d), want (true, 2)", anyBusy, busyCount)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return")
	}

	// block.Tracker should have advanced to the synthesised block id. Read
	// bt.ID() only after RunWith has returned: the tick loop calls
	// bt.Update() (the sole writer), so reading mid-run would race it.
	if bt.ID() != "2026-05-20T14Z" {
		t.Errorf("block.id = %q, want 2026-05-20T14Z", bt.ID())
	}

	_ = hitCount.Load()
}

// TestCaffeinate_TogglePersistsAcrossTicks is a regression test for the bug
// where calling the Caffeinate(on) RPC while no agents are working caused the
// TUI indicator to revert to OFF on the very next tick.
//
// Root cause: lifecycle.go computed active := newState != caffeinate.StateOff,
// but Manager.Tick(anyWorking=false) leaves state==StateOff when the toggle is
// on-but-idle (it waits for agents to start before spawning). The next tick
// then called setCaffeinateActive(false), overwriting the RPC handler's
// synchronous setCaffeinateActive(true).
//
// Fix: active := newState != caffeinate.StateOff || toggleOn, so the indicator
// reflects the user's intent even when the subprocess hasn't spawned yet.
func TestCaffeinate_TogglePersistsAcrossTicks(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	// in-memory SQLite + WriteService + ReadService
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

	rs := service.NewReadService(service.ReadDeps{
		Sessions: sqlite.NewSessionStore(db),
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})

	// Caffeinate manager with no-op Spawn/Kill so the test doesn't try to
	// launch a real caffeinate(1) process.
	caffMgr := &caffeinate.Manager{
		Grace:   60 * time.Second,
		Spawn:   func(int) error { return nil },
		Kill:    func() error { return nil },
		IsAlive: nil,
		Now:     time.Now,
	}

	// tickSeen is closed after the first tick fires so we can advance to it.
	tickSeen := make(chan struct{})
	var tickSeenOnce sync.Once

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:        paths,
			Tick:         30 * time.Millisecond,
			Caffeinate:   caffMgr,
			WriteService: ws,
			ReadService:  rs,
			// stubPoller: no working sessions (anyWorking=false every tick).
			Poller: &stubPoller{
				snapshot: func(ctx context.Context) (*aggregate.Tree, bool, error) {
					tickSeenOnce.Do(func() { close(tickSeen) })
					return &aggregate.Tree{}, false, nil
				},
			},
		})
	}()

	waitForFile(t, paths.Socket)

	conn := dialUnix(t, paths.Socket)
	defer func() { _ = conn.Close() }()
	client := pb.NewPaMonitorClient(conn)

	// Flip caffeinate ON while no agents are working.
	resp, err := client.Caffeinate(context.Background(), &pb.CaffeinateRequest{Action: "on"})
	if err != nil {
		t.Fatalf("Caffeinate RPC: %v", err)
	}
	if !resp.GetActive() {
		t.Fatal("Caffeinate RPC: Active = false immediately after toggling on")
	}

	// Wait for at least one tick to fire (anyWorking=false every tick).
	select {
	case <-tickSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first tick")
	}
	// Allow time for a second tick to ensure the gauge has been reconciled.
	time.Sleep(100 * time.Millisecond)

	// The key assertion: caffeinate_active must still be true after tick
	// reconciliation even though no agents are running.
	state, err := client.GetState(context.Background(), &pb.GetStateRequest{})
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !state.GetCaffeinateActive() {
		t.Error("caffeinate_active reverted to false after tick — toggle did not persist across ticks (regression)")
	}
	// Two-indicator (D6) distinction: this is the incident shape — MODE on
	// (the user armed auto-caffeinate) but PROCESS off (no agents working, so
	// nothing is holding the assertion). The two must read distinctly.
	if !state.GetCaffeinateMode() {
		t.Error("caffeinate_mode = false after toggling on; want true (the user toggle)")
	}
	if state.GetCaffeinateProcess() != pb.CaffeinateProcess_CAFFEINATE_PROCESS_OFF {
		t.Errorf("caffeinate_process = %v; want OFF (incident shape: armed, not holding)", state.GetCaffeinateProcess())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return after cancel")
	}
}

// stubPoller moved to pollerstub_test.go (untagged) — nudger_lifecycle_test.go
// (unit) uses it too (bead pg2-h05lt).

// TestTickIntegration_WritesBlocksAndContributions verifies that when
// WriteService is wired into RunOptions:
//   - The active block from ccusage is persisted to the blocks table after
//     the first tick.
//   - Per-session contributions are persisted to session_block_contributions
//     after the second tick (ActiveBlockID is propagated between ticks).
func TestTickIntegration_WritesBlocksAndContributions(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	// --- in-memory SQLite + WriteService ---
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

	// --- session fixture on disk ---
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessJSON, _ := json.Marshal(map[string]any{
		"sessionId": "test-sess-1",
		"pid":       os.Getpid(), // use current PID so PidAlive returns true
		"cwd":       dir,
		"kind":      "project",
		"startedAt": time.Now().UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(sessDir, "test-sess-1.json"), sessJSON, 0o600); err != nil {
		t.Fatalf("write session fixture: %v", err)
	}

	// --- active-block fixture: a recent assistant usage record so the poller's
	// (auto-wired) corpus Monitor UsagePricing observer produces a non-nil active
	// block for the session's transcript. ---
	recTS := time.Now().Add(-30 * time.Minute)
	projSlug := strings.NewReplacer("/", "-", "_", "-").Replace(dir)
	projDir := filepath.Join(dir, "projects", projSlug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	transcript := `{"type":"assistant","timestamp":"` + recTS.Format(time.RFC3339Nano) +
		`","message":{"model":"m","role":"assistant","usage":{"input_tokens":1000,"output_tokens":500}}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "test-sess-1.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatalf("write transcript fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// --- real *poller.Poller wired with DB and WriteService. The poller lazily
	// builds a corpus Monitor over SessionsDir/ClaudeHome (buildPoller wires one in
	// production); the block comes from that Monitor, not an injected pricer. ---
	p := &poller.Poller{
		SessionsDir:  sessDir,
		ClaudeHome:   dir,
		PidAlive:     func(pid int) bool { return pid == os.Getpid() },
		Now:          time.Now,
		WriteService: ws,
		DB:           db,
	}

	tickCount := 0
	tickSynced := make(chan struct{}, 10)

	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:        paths,
			Tick:         30 * time.Millisecond,
			Poller:       p,
			WriteService: ws,
			// TreeObserver fires after each tick so we can sync and count.
			TreeObserver: func(_ *aggregate.Tree) {
				tickCount++
				// Sync the write service so DB queries below see committed rows.
				_ = ws.Sync(ctx)
				if tickCount <= 3 {
					select {
					case tickSynced <- struct{}{}:
					default:
					}
				}
			},
		})
	}()

	waitForFile(t, paths.Socket)

	// Wait for at least 2 ticks (tick 1 writes block; tick 2 writes contributions).
	for i := 0; i < 2; i++ {
		select {
		case <-tickSynced:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for tick %d", i+1)
		}
	}

	// --- assert blocks table: the daemon persisted the Monitor's active block ---
	var blockID int64
	var blockStringID string
	err = db.QueryRowContext(context.Background(),
		"SELECT id, block_id FROM blocks ORDER BY id DESC LIMIT 1").Scan(&blockID, &blockStringID)
	if err == sql.ErrNoRows {
		t.Fatal("blocks table: expected an active-block row from the Monitor, got none")
	}
	if err != nil {
		t.Fatalf("blocks query: %v", err)
	}
	if blockStringID == "" {
		t.Errorf("persisted block_id is empty, want the Monitor's block id")
	}

	// Wait for one more tick to ensure contributions land (they need ActiveBlockID set).
	select {
	case <-tickSynced:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for contribution tick")
	}

	// --- assert session_block_contributions ---
	var contribCount int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM session_block_contributions WHERE block_id = ?", blockID).Scan(&contribCount); err != nil {
		t.Fatalf("contributions query: %v", err)
	}
	if contribCount == 0 {
		t.Errorf("session_block_contributions: expected >= 1 row for block id %d, got 0", blockID)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return")
	}
}
