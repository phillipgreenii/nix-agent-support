package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// TestGetState_SizeIndependentOfStaleSessions guards the status --json
// failure where a daemon holding thousands of stale (pid-less) sessions
// produced a reply past the gRPC receive cap. It runs a REAL daemon over a
// real unix socket and a real SQLite store holding 3000 stale sessions plus 3
// live ones, then asserts the size and time bounds of the three answers:
// default (large, proving the fixture is meaningful), live_sessions_only and
// rate_limits_only (small, the latter independent of session count).
// loadBound is a deliberately generous ceiling for waits in this test. The
// assertions guard size, not speed; the bound exists only so a hung daemon
// fails instead of blocking forever, and must stay far above anything a
// heavily loaded host can cause (load averages near 100 stretched a 2s bound
// past 40s).
const loadBound = 5 * time.Minute

func TestGetState_SizeIndependentOfStaleSessions(t *testing.T) {
	const stale, live = 3000, 3
	dir := shortTempDir(t)
	paths := Paths{Dir: dir, PIDFile: filepath.Join(dir, "daemon.pid"), Socket: filepath.Join(dir, "daemon.sock")}

	db, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ss, bs, cs := sqlite.NewSessionStore(db), sqlite.NewBlockStore(db), sqlite.NewContributionStore(db)
	now := time.Now().UTC()
	blockID, err := bs.Upsert(ctx, store.Block{
		BlockID: "blk", StartedAt: now.Add(-time.Hour), EndedAt: now.Add(time.Hour),
		LastProcessedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("block upsert: %v", err)
	}
	for i := 0; i < stale+live; i++ {
		sid := fmt.Sprintf("session-%05d", i)
		sess := store.Session{
			SessionID: sid, Cwd: fmt.Sprintf("/work/project-%d/with/a/reasonably/long/path", i%200),
			Status: "idle", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now,
		}
		if i >= stale {
			pid := 1000 + i
			sess.PID = &pid
		}
		if err := ss.Upsert(ctx, sess); err != nil {
			t.Fatalf("session upsert: %v", err)
		}
		var id int64
		if err := db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id=?", sid).Scan(&id); err != nil {
			t.Fatalf("session id: %v", err)
		}
		if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: id, ParentID: blockID, CostUSD: 1, Tokens: 10, UpdatedAt: now}); err != nil {
			t.Fatalf("contribution: %v", err)
		}
	}
	rs := service.NewReadService(service.ReadDeps{
		Sessions: ss, Blocks: bs, Weeks: sqlite.NewWeekStore(db),
		Toggles: sqlite.NewToggleStore(db), Nudges: sqlite.NewNudgeStore(db),
	})

	five := 61.5
	p := &fakeMonitorPoller{lim: &Limits{FiveHourPct: &five, FiveHourResetsAt: now.Add(time.Hour).Truncate(time.Second), CapturedAt: now}}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- RunWith(runCtx, RunOptions{Paths: paths, Tick: 20 * time.Millisecond, Poller: p, ReadService: rs})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(loadBound):
			t.Errorf("RunWith did not return within %v after cancel", loadBound)
		}
	})
	waitForFile(t, paths.Socket)
	conn := dialUnix(t, paths.Socket)
	defer func() { _ = conn.Close() }()
	client := pb.NewPaMonitorClient(conn)

	get := func(req *pb.GetStateRequest) (*pb.DaemonState, time.Duration) {
		t.Helper()
		start := time.Now()
		st, err := client.GetState(ctx, req)
		if err != nil {
			t.Fatalf("GetState(%v): %v", req, err)
		}
		return st, time.Since(start)
	}
	sessionCount := func(st *pb.DaemonState) int {
		n := 0
		for _, d := range st.GetDirs() {
			n += len(d.GetSessions())
		}
		return n
	}

	// Wait for the tick to refresh the cache (limits overlay present).
	var full *pb.DaemonState
	for deadline := time.Now().Add(loadBound); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		full, _ = get(&pb.GetStateRequest{})
		if full.FiveHourPct != nil {
			break
		}
	}
	if got := sessionCount(full); got != stale+live {
		t.Fatalf("default answer holds %d sessions, want %d", got, stale+live)
	}
	fullSize := proto.Size(full)
	if fullSize < 200_000 {
		t.Fatalf("fixture too small to be meaningful: default answer is %d bytes", fullSize)
	}

	liveSt, liveDur := get(&pb.GetStateRequest{LiveSessionsOnly: true})
	if got := sessionCount(liveSt); got != live {
		t.Errorf("live_sessions_only holds %d sessions, want %d", got, live)
	}
	if sz := proto.Size(liveSt); sz > 16<<10 {
		t.Errorf("live_sessions_only answer is %d bytes, want <= 16 KiB", sz)
	}

	light, lightDur := get(&pb.GetStateRequest{RateLimitsOnly: true})
	if got := len(light.GetDirs()); got != 0 {
		t.Errorf("rate_limits_only carries %d dirs, want 0", got)
	}
	if sz := proto.Size(light); sz > 1<<10 {
		t.Errorf("rate_limits_only answer is %d bytes, want <= 1 KiB", sz)
	}
	if light.FiveHourPct == nil || light.GetFiveHourPct() != five {
		t.Errorf("rate_limits_only five_hour_pct = %v, want %v", light.FiveHourPct, five)
	}
	for name, d := range map[string]time.Duration{"live_sessions_only": liveDur, "rate_limits_only": lightDur} {
		if d > loadBound {
			t.Errorf("%s took %v, want <= %v", name, d, loadBound)
		}
	}
}
