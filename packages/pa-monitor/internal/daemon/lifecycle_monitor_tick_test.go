package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// fakeMonitorPoller stands in for a corpus-Monitor-backed poller: it serves the
// limits/weekly projections the lifecycle reads (via MonitorLimits/MonitorWeekly).
type fakeMonitorPoller struct {
	mu        sync.Mutex
	block     *usage.Block
	lim       *Limits
	week      *usage.WeeklyEntry
	weekCalls int
}

func (f *fakeMonitorPoller) Snapshot(context.Context) (*aggregate.Tree, bool, error) {
	return &aggregate.Tree{ActiveBlock: f.block}, true, nil
}
func (f *fakeMonitorPoller) MonitorLimits() *Limits { return f.lim }
func (f *fakeMonitorPoller) MonitorWeekly(time.Time) *usage.WeeklyEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.weekCalls++
	return f.week
}
func (f *fakeMonitorPoller) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.weekCalls }

// TestTick_MonitorProjectionsRoutedAndCadence proves the daemon tick reads
// rate_limits + weekly from the poller's Monitor projections: (a) the tree's
// rate_limits come from MonitorLimits every tick; (b) the weekly read honors
// WeeklyEvery (not read every tick), preserving the pre-fold DB-write cadence.
func TestTick_MonitorProjectionsRoutedAndCadence(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{Dir: dir, PIDFile: filepath.Join(dir, "daemon.pid"), Socket: filepath.Join(dir, "daemon.sock")}

	fivePct := 42.0
	p := &fakeMonitorPoller{
		block: &usage.Block{ID: "blk", IsActive: true, CostUSD: 1},
		lim:   &Limits{FiveHourPct: &fivePct, CapturedAt: time.Unix(1_700_000_000, 0)},
		week:  &usage.WeeklyEntry{Period: "2026-07-20", TotalCost: 9.0},
	}

	var (
		mu    sync.Mutex
		last  *aggregate.Tree
		ticks int
	)
	ticked := make(chan struct{}, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:       paths,
			Tick:        20 * time.Millisecond,
			Poller:      p,
			WeeklyEvery: 3, // weekly read only on tickCount % 3 == 0
			TreeObserver: func(tr *aggregate.Tree) {
				mu.Lock()
				last = tr
				ticks++
				mu.Unlock()
				select {
				case ticked <- struct{}{}:
				default:
				}
			},
		})
	}()

	// Wait for ~7 ticks so several WeeklyEvery windows elapse.
	for i := 0; i < 7; i++ {
		select {
		case <-ticked:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for tick %d", i+1)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return")
	}

	mu.Lock()
	gotTree, gotTicks := last, ticks
	mu.Unlock()

	if gotTree == nil || gotTree.FiveHourPct == nil || *gotTree.FiveHourPct != 42 {
		t.Fatalf("tree FiveHourPct = %v, want 42 from the Monitor projection", gotTree)
	}
	wc := p.calls()
	if wc == 0 {
		t.Errorf("MonitorWeekly never called; weekly not read from the Monitor")
	}
	if wc >= gotTicks {
		t.Errorf("MonitorWeekly called %d times over %d ticks; WeeklyEvery=3 cadence not honored (should be ~1/3)", wc, gotTicks)
	}
}
