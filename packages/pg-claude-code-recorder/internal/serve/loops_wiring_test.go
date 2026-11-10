package serve

import (
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// fakeTicks is the Daemon.ticker and Daemon.timer seam for tests: each loop
// gets one unbuffered channel the test sends on, so a loop body runs exactly
// when the test says and nothing sleeps.
type fakeTicks struct {
	mu   sync.Mutex
	chs  map[string]chan time.Time
	done chan string
}

func newFakeTicks(d *Daemon) *fakeTicks {
	f := &fakeTicks{chs: map[string]chan time.Time{}, done: make(chan string, 256)}
	src := func(loop string, _ time.Duration) (<-chan time.Time, func()) { return f.ch(loop), func() {} }
	d.tickerFn, d.timerFn = src, src
	d.loopDone = func(l string) { f.done <- l }
	return f
}

func (f *fakeTicks) ch(loop string) chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chs[loop] == nil {
		f.chs[loop] = make(chan time.Time)
	}
	return f.chs[loop]
}

// fire delivers one tick to the loop and waits until the loop has recorded
// `passes` passes of the named loops (each name once).
func (f *fakeTicks) fire(t *testing.T, tickLoop string, recorded ...string) {
	t.Helper()
	select {
	case f.ch(tickLoop) <- time.Time{}:
	case <-time.After(10 * time.Second):
		t.Fatalf("loop %s did not take the tick", tickLoop)
	}
	f.await(t, recorded...)
}

func (f *fakeTicks) await(t *testing.T, recorded ...string) {
	t.Helper()
	want := map[string]int{}
	for _, r := range recorded {
		want[r]++
	}
	for len(want) > 0 {
		select {
		case l := <-f.done:
			if want[l] > 0 {
				want[l]--
				if want[l] == 0 {
					delete(want, l)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("passes not recorded: %v", want)
		}
	}
}

// startLoop starts one production loop goroutine (the entry point Start launches)
// through the daemon's goLoop, so Close waits for it.
func startLoop(d *Daemon, loop func()) {
	d.goLoop("test", loop)
}

func seamDaemon(t *testing.T, mod func(*env)) (*env, *Daemon, *fakeTicks) {
	t.Helper()
	e := newEnv(t, true)
	if mod != nil {
		mod(e)
	}
	d := e.open(t)
	f := newFakeTicks(d)
	t.Cleanup(func() { _ = d.Close() })
	return e, d, f
}

// Each production loop body is driven once through its real loop function (the
// goroutine body Start launches, fed by the ticker seam) and its run counter
// rises by exactly one.
func TestEachProductionLoopRecordsExactlyOneRunPerTick(t *testing.T) {
	cases := []struct {
		name   string
		tick   string   // the seam name the test fires
		passes []string // loops the tick records
		run    func(*Daemon)
		mod    func(*env)
	}{
		{"settle tick and its removal pass", obs.LoopSettleTick, []string{obs.LoopRemovalPass, obs.LoopSettleTick}, (*Daemon).settleLoop, func(e *env) { e.cfg.SettleMode = "scan"; e.cfg.RemoveEnable = true }},
		{"refresh gauges", obs.LoopRefreshGauges, []string{obs.LoopRefreshGauges}, (*Daemon).refreshLoop, nil},
		{"wal checkpoint", obs.LoopWALCheckpoint, []string{obs.LoopWALCheckpoint}, (*Daemon).walCheckpointLoop, nil},
		{"tracker tick and poll", obs.LoopTrackerTick, []string{obs.LoopTrackerTick, obs.LoopTrackerPoll}, (*Daemon).trackLoop, nil},
		{"doctor", obs.LoopDoctor, []string{obs.LoopDoctor}, (*Daemon).doctorLoop, func(e *env) { e.cfg.DoctorInterval = time.Hour }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, d, f := seamDaemon(t, c.mod)
			startLoop(d, func() { c.run(d) })
			f.fire(t, c.tick, c.passes...)
			for _, l := range c.passes {
				if got := loopRuns(d, l, "ok") + loopRuns(d, l, "error"); got != 1 {
					t.Errorf("%s runs = %v after one tick, want 1", l, got)
				}
				if h := d.obs.Reg.HistL(obs.MLoopDuration, loopLabels(l)); h == nil || h.Samples != 1 {
					t.Errorf("%s duration samples %+v, want 1", l, h)
				}
			}
		})
	}
}

func TestStatusLoopRecordsAPassPerTimer(t *testing.T) {
	_, d, f := seamDaemon(t, nil)
	startLoop(d, d.statusLoop)
	f.await(t, obs.LoopStatusRefresh) // the first pass runs at once
	if got := loopRuns(d, obs.LoopStatusRefresh, "ok"); got != 1 {
		t.Fatalf("after start: %v runs, want 1", got)
	}
	f.fire(t, obs.LoopStatusRefresh, obs.LoopStatusRefresh)
	if got := loopRuns(d, obs.LoopStatusRefresh, "ok"); got != 2 {
		t.Fatalf("after one timer: %v runs, want 2", got)
	}
}

func TestParserDrainAndSweepAndIngestAreRecordedThroughTheirEntryPoints(t *testing.T) {
	e, d, f := seamDaemon(t, nil)
	p := e.transcript("a.jsonl", "{\"type\":\"user\"}\n", time.Now().Add(-time.Hour))
	startLoop(d, d.parseLoop)
	// watcher_sweep: a forced sweep (the Rescan entry point) is one pass that
	// includes the per-file ingest it triggers.
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	f.await(t, obs.LoopWatcherSweep)
	if got := loopRuns(d, obs.LoopWatcherSweep, "ok"); got != 1 {
		t.Fatalf("sweep runs = %v, want 1", got)
	}
	if h := d.obs.Reg.HistL(obs.MLoopDuration, ingestLabels); h == nil || h.Samples != 1 {
		t.Fatalf("copier_ingest samples %+v for the one file %s, want 1", h, p)
	}
	// parser_drain: the ingest woke the parse loop, which drains once.
	f.await(t, obs.LoopParserDrain)
	if got := loopRuns(d, obs.LoopParserDrain, "ok"); got != 1 {
		t.Fatalf("parser_drain runs = %v, want 1", got)
	}
}
