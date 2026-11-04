package serve

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

func loopRuns(d *Daemon, loop, outcome string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MLoopRuns, map[string]string{"loop": loop, "outcome": outcome})
	return v
}

func loopInFlight(d *Daemon, loop string) float64 {
	v, _ := d.obs.Reg.Gauge(obs.MLoopInFlight, loopLabels(loop))
	return v
}

func TestRunLoopRecordsOneRunPerLoopWithItsOutcome(t *testing.T) {
	for _, loop := range obs.PassLoops() {
		t.Run(loop, func(t *testing.T) {
			d, clk, _ := tickEnv()
			if err := d.runLoop(loop, func() error { clk.Advance(2 * time.Second); return nil }); err != nil {
				t.Fatal(err)
			}
			boom := errors.New("boom")
			if err := d.runLoop(loop, func() error { return boom }); !errors.Is(err, boom) {
				t.Fatalf("error not returned: %v", err)
			}
			if loopRuns(d, loop, "ok") != 1 || loopRuns(d, loop, "error") != 1 {
				t.Fatalf("ok=%v error=%v", loopRuns(d, loop, "ok"), loopRuns(d, loop, "error"))
			}
			h := d.obs.Reg.HistL(obs.MLoopDuration, loopLabels(loop))
			if h == nil || h.Samples != 2 || h.Sum != 2 {
				t.Fatalf("duration %+v", h)
			}
			want := float64(clk.Now().UnixNano()) / 1e9
			if v, _ := d.obs.Reg.Gauge(obs.MLoopLastTimestamp, loopLabels(loop)); v != want {
				t.Fatalf("last timestamp %v, want %v", v, want)
			}
		})
	}
}

func TestEveryLoopValueIsRegisteredAtZero(t *testing.T) {
	d, _, _ := tickEnv()
	if len(obs.Loops) != 11 {
		t.Fatalf("%d loop values", len(obs.Loops))
	}
	for _, l := range obs.Loops {
		if h := d.obs.Reg.HistL(obs.MLoopDuration, loopLabels(l)); h == nil || h.Samples != 0 {
			t.Errorf("duration series of %s: %+v", l, h)
		}
	}
	for _, l := range obs.PassLoops() {
		if _, ok := d.obs.Reg.Gauge(obs.MLoopInFlight, loopLabels(l)); !ok {
			t.Errorf("in-flight series of %s missing", l)
		}
	}
}

func TestLoopInFlightIsNonZeroWhileAStepRunsAndZeroAfter(t *testing.T) {
	d, clk, _ := tickEnv()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = d.runLoop(obs.LoopStatusRefresh, func() error { close(started); <-release; return nil })
	}()
	<-started
	clk.Advance(30 * time.Second)
	d.refreshLoopsInFlight() // what a scrape runs
	if v := loopInFlight(d, obs.LoopStatusRefresh); v != 30 {
		t.Fatalf("in flight while blocked = %v, want 30", v)
	}
	// A second, concurrent pass of the same loop keeps the OLDEST start.
	clk.Advance(10 * time.Second)
	id := d.loops.begin(obs.LoopStatusRefresh, clk.Now())
	d.refreshLoopsInFlight()
	if v := loopInFlight(d, obs.LoopStatusRefresh); v != 40 {
		t.Fatalf("with two passes = %v, want 40 (the oldest)", v)
	}
	d.loops.end(obs.LoopStatusRefresh, id)
	if v := loopInFlight(d, obs.LoopRefreshGauges); v != 0 {
		t.Fatalf("an idle loop reports %v", v)
	}
	close(release)
	<-done
	if v := loopInFlight(d, obs.LoopStatusRefresh); v != 0 {
		t.Fatalf("in flight after the pass = %v, want 0", v)
	}
	d.refreshLoopsInFlight()
	if v := loopInFlight(d, obs.LoopStatusRefresh); v != 0 {
		t.Fatalf("in flight after a scrape = %v, want 0", v)
	}
}

// A scrape refreshes the in-flight gauges itself, so a stalled refresh loop
// still reports.
func TestScrapeRefreshReportsAStalledLoop(t *testing.T) {
	d, clk, _ := tickEnv()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = d.runLoop(obs.LoopRefreshGauges, func() error { close(started); <-release; return nil })
	}()
	<-started
	clk.Advance(90 * time.Second)
	d.scrapeRefresh()
	if v := loopInFlight(d, obs.LoopRefreshGauges); v != 90 {
		t.Fatalf("a scrape reports %v, want 90", v)
	}
	close(release)
	<-done
	d.scrapeRefresh()
	if v := loopInFlight(d, obs.LoopRefreshGauges); v != 0 {
		t.Fatalf("after the pass: %v, want 0", v)
	}
}

func TestLoopWrapperAddsNoStoreQuery(t *testing.T) {
	c := countdriver.Register("loops-counting")
	e := newEnv(t, false)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: "loops-counting"})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	defer func() { _ = d.Close() }()
	c.Reset()
	for _, l := range obs.PassLoops() {
		_ = d.runLoop(l, func() error { return nil })
	}
	d.refreshLoopsInFlight()
	if n := c.Queries(); n != 0 {
		t.Fatalf("the loop wrapper sent %d store queries, want 0", n)
	}
}

// The per-event copier_ingest path adds at most two registry lock acquisitions
// per call (one histogram observation here, the results counter later). The
// copier's own lag gauges are not the wrapper's, so the test ingests a file that
// is already copied and compares with the copier alone.
func TestIngestTakesTheRegistryLockAtMostTwice(t *testing.T) {
	e := newEnv(t, false)
	p := e.transcript("a.jsonl", "{\"type\":\"user\"}\n", time.Now().Add(-time.Hour))
	d := e.open(t)
	defer func() { _ = d.Close() }()
	d.ingest(p) // copies it
	var mu sync.Mutex
	locks := 0
	count := func(f func()) int {
		mu.Lock()
		locks = 0
		mu.Unlock()
		f()
		mu.Lock()
		defer mu.Unlock()
		return locks
	}
	d.obs.Reg.OnLock = func() { mu.Lock(); locks++; mu.Unlock() }
	base := count(func() { _, _ = d.cp.Ingest(p) })
	n := count(func() { d.ingest(p) })
	t.Logf("registry lock acquisitions per ingest: copier alone %d, with the wrapper %d", base, n)
	// The bound is on what the loop wrapper adds: one now, two with the results
	// counter. The copier's own lag gauges (base) are not the wrapper's.
	if added := n - base; added < 1 || added > 2 {
		t.Fatalf("the wrapper added %d registry lock acquisitions (copier alone %d, total %d), want 1 or 2", added, base, n)
	}
	if v, _ := d.obs.Reg.Counter(obs.MLoopRuns, map[string]string{"loop": obs.LoopCopierIngest, "outcome": obs.OutcomeOK}); v != 0 {
		t.Fatal("copier_ingest wrote a loop_runs_total series")
	}
	if v, _ := d.obs.Reg.Gauge(obs.MLoopLastTimestamp, ingestLabels); v != 0 {
		t.Fatal("copier_ingest wrote a last timestamp")
	}
	if v, _ := d.obs.Reg.Gauge(obs.MLoopInFlight, ingestLabels); v != 0 {
		t.Fatal("copier_ingest wrote an in-flight value")
	}
}
