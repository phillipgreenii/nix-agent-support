package serve

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// loopTracker holds the start times of the passes now running, per loop, so the
// oldest one can be reported as loop_in_flight_seconds. The zero value is ready.
type loopTracker struct {
	mu     sync.Mutex
	seq    uint64
	starts map[string]map[uint64]time.Time
}

// begin records a pass of loop starting at t and returns its id.
func (lt *loopTracker) begin(loop string, t time.Time) uint64 {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.starts == nil {
		lt.starts = map[string]map[uint64]time.Time{}
	}
	if lt.starts[loop] == nil {
		lt.starts[loop] = map[uint64]time.Time{}
	}
	lt.seq++
	lt.starts[loop][lt.seq] = t
	return lt.seq
}

// end forgets the pass id of loop.
func (lt *loopTracker) end(loop string, id uint64) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.starts[loop], id)
}

// oldest is the start of the oldest pass of loop still running.
func (lt *loopTracker) oldest(loop string) (time.Time, bool) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	var first time.Time
	found := false
	for _, t := range lt.starts[loop] {
		if !found || t.Before(first) {
			first, found = t, true
		}
	}
	return first, found
}

func loopLabels(loop string) map[string]string { return map[string]string{"loop": loop} }

// ingestLabels is the label set of the per-event copier_ingest observation (it
// is read-only, so every call shares it).
var ingestLabels = loopLabels(obs.LoopCopierIngest)

// runLoop runs one pass of the named loop through step and is the one place
// that records a pass: its duration (loop_duration_seconds), its outcome
// (loop_runs_total), the time it ended (loop_last_timestamp_seconds) and, while
// it runs, its age (loop_in_flight_seconds). It adds one clock read pair, one
// histogram observation, one counter add and one gauge set, and no store query.
// It returns step's error. The per-event copier_ingest loop does not use it.
func (d *Daemon) runLoop(name string, step func() error) error {
	start := d.clock.Now()
	id := d.loops.begin(name, start)
	d.setLoopInFlight(name)
	var err error
	defer func() {
		d.loops.end(name, id)
		d.setLoopInFlight(name)
	}()
	err = step()
	end := d.clock.Now()
	outcome := obs.OutcomeOK
	if err != nil {
		outcome = obs.OutcomeError
	}
	reg := d.obs.Reg
	reg.ObserveL(obs.MLoopDuration, loopLabels(name), end.Sub(start).Seconds())
	reg.Add(obs.MLoopRuns, map[string]string{"loop": name, "outcome": outcome}, 1)
	reg.Set(obs.MLoopLastTimestamp, loopLabels(name), float64(end.UnixNano())/1e9)
	if d.loopDone != nil {
		d.loopDone(name)
	}
	return err
}

// setLoopInFlight sets loop_in_flight_seconds{loop} to the age of the oldest
// running pass of the loop, 0 when none runs.
func (d *Daemon) setLoopInFlight(name string) {
	var age float64
	if t, ok := d.loops.oldest(name); ok {
		age = d.clock.Now().Sub(t).Seconds()
	}
	d.obs.Reg.Set(obs.MLoopInFlight, loopLabels(name), age)
}

// refreshLoopsInFlight refreshes every loop's in-flight gauge. It reads only
// process-local state (no store read), so a scrape can run it.
func (d *Daemon) refreshLoopsInFlight() {
	for _, l := range obs.PassLoops() {
		d.setLoopInFlight(l)
	}
}

// ticker returns a channel that fires every `every` and the function that stops
// it; loop names the caller so a test can drive each loop apart. It is the seam the periodic loops take their cadence from: production uses
// a real ticker, tests replace it so a loop body runs without waiting.
func (d *Daemon) ticker(loop string, every time.Duration) (<-chan time.Time, func()) {
	if d.tickerFn != nil {
		return d.tickerFn(loop, every)
	}
	t := time.NewTicker(every)
	return t.C, t.Stop
}

// timer is the one-shot counterpart of ticker, for the loops that wait between
// the end of one pass and the start of the next.
func (d *Daemon) timer(loop string, wait time.Duration) (<-chan time.Time, func()) {
	if d.timerFn != nil {
		return d.timerFn(loop, wait)
	}
	t := time.NewTimer(wait)
	return t.C, func() { t.Stop() }
}
