package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// TestServedState_CarriesRateLimitsWithoutAnActiveBlock guards the early-release
// defect of bead pg2-ip6r5. The served DaemonState is materialised from the DB,
// and the status-line rate_limits used to reach it ONLY through the persisted
// active block row. The block is a ccusage-style hour-floored 5h cost block (e.g.
// 03:00-08:00Z) while the server's window is anchored at the account's own
// instant (resets 08:30Z). Once the hour-floored block ends there is no active
// block until the account spends again, and a window at its limit cannot spend,
// so for the last half hour of every limited window `status --json` dropped
// rate_limits, ccpool's usage gate read "unknown", failed open, and admitted
// work into a window that was still at 100 percent.
//
// The live Monitor reading is independent of the block, so it MUST still be
// served when the DB holds no active block.
func TestServedState_CarriesRateLimitsWithoutAnActiveBlock(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{Dir: dir, PIDFile: filepath.Join(dir, "daemon.pid"), Socket: filepath.Join(dir, "daemon.sock")}

	// An empty DB: no block row, exactly the 08:00-08:30Z shape.
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rs := service.NewReadService(service.ReadDeps{
		Sessions: sqlite.NewSessionStore(db),
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})

	fivePct := 100.0
	resets := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	p := &fakeMonitorPoller{ // no active block, only the Monitor's limits reading
		lim: &Limits{FiveHourPct: &fivePct, FiveHourResetsAt: resets, CapturedAt: time.Now().UTC()},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:       paths,
			Tick:        20 * time.Millisecond,
			Poller:      p,
			ReadService: rs,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("RunWith did not return after cancel")
		}
	})

	waitForFile(t, paths.Socket)
	conn := dialUnix(t, paths.Socket)
	defer func() { _ = conn.Close() }()
	client := pb.NewPaMonitorClient(conn)

	// Poll across several ticks: the tick refreshes the cached snapshot.
	var state *pb.DaemonState
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, err = client.GetState(context.Background(), &pb.GetStateRequest{})
		if err != nil {
			t.Fatalf("GetState: %v", err)
		}
		if state.FiveHourPct != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if state.FiveHourPct == nil || state.GetFiveHourPct() != 100 {
		t.Fatalf("served five_hour_pct = %v, want 100 from the Monitor reading with no active block", state.FiveHourPct)
	}
	if got := state.GetFiveHourResetsAt().AsTime(); !got.Equal(resets) {
		t.Errorf("served five_hour_resets_at = %v, want %v", got, resets)
	}
}
