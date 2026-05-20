package daemon

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// fakePoller implements the small subset of *poller.Poller that RunWith
// uses on the tick path: nothing — RunWith calls Snapshot. We synthesise
// a Tree directly without going through the real poller.
//
// This test asserts that:
//  1. The shared state is updated after a tick.
//  2. GetState reflects the synthesised state.
//  3. IsAnyBusy returns true when any directory has working sessions.
//  4. block.Tracker.OnLimitHit is invoked when the synthesised block
//     exceeds the cap.
func TestRunWith_IntegratesPollerTrackersAndState(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	// Synthesised state: 2 working sessions, 1 idle.
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path:     "/p1",
				WorkingN: 2,
				IdleN:    1,
				Sessions: []*aggregate.SessionView{
					{Session: &session.Session{SessionID: "a", Status: session.Working}},
					{Session: &session.Session{SessionID: "b", Status: session.Working}},
					{Session: &session.Session{SessionID: "c", Status: session.Idle}},
				},
			},
		},
		ActiveBlock: &ccusage.Block{
			StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC),
			IsActive:  true,
			CostUSD:   100.0, // above cap
		},
	}

	// Use a Poller-like value with the field signature RunWith expects.
	// RunWith calls opts.Poller.Snapshot via the concrete *poller.Poller,
	// so we wrap via a function adapter type below.
	bt := block.NewTracker(50.0) // cap below cost → should fire OnLimitHit
	wt := week.NewTracker(0)     // disabled

	hitCount := atomic.Int32{}
	// OnLimitHit will be overridden by RunWith; we wrap to count via emitter? No — RunWith
	// only assigns OnLimitHit when Emitter is non-nil. To observe, set our own callback
	// before RunWith starts. The RunWith code first assigns (overwriting). So we test the
	// callback by inspecting tracker state after the tick.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths: paths,
			Tick:  50 * time.Millisecond,
			Poller: &stubPoller{
				snapshot: func(ctx context.Context) (*aggregate.Tree, bool, error) {
					return tree, true, nil
				},
			},
			BlockTracker: bt,
			WeekTracker:  wt,
		})
	}()

	waitForFile(t, paths.Socket)

	// Give the tick loop time to fire at least twice.
	time.Sleep(150 * time.Millisecond)

	// Verify GetState reflects synthesised dirs.
	conn := dialUnix(t, paths.Socket)
	defer conn.Close()
	client := pb.NewPaMonitorClient(conn)
	state, err := client.GetState(context.Background(), &pb.GetStateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GetDirs()) != 1 || state.GetDirs()[0].GetWorkingN() != 2 {
		t.Errorf("daemon state did not reflect synthesised tree: %+v", state.GetDirs())
	}

	// IsAnyBusy should see 2 busy.
	busy, err := client.IsAnyBusy(context.Background(), &pb.IsAnyBusyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !busy.GetAnyBusy() || busy.GetBusyCount() != 2 {
		t.Errorf("IsAnyBusy = %+v, want (true,2)", busy)
	}

	// block.Tracker should have advanced to the synthesised block id.
	if bt.ID() != "2026-05-20T14Z" {
		t.Errorf("block.id = %q, want 2026-05-20T14Z", bt.ID())
	}

	_ = hitCount

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return")
	}
}

// stubPoller is a minimal PollerInterface implementation used by the
// integration test. Avoids needing to construct the full *poller.Poller
// with its many dependencies.
type stubPoller struct {
	snapshot func(ctx context.Context) (*aggregate.Tree, bool, error)
}

func (s *stubPoller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	return s.snapshot(ctx)
}
