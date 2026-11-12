package serve

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// statusRefreshEvery is the pause between two recomputations of the status
// figures (measured from the end of one run to the start of the next).
const statusRefreshEvery = 5 * time.Second

// statusFirstWait is how long a status call waits for the first snapshot
// before it answers "still being computed".
const statusFirstWait = 500 * time.Millisecond

// statusSnap is what status serves: the copy and parse lag figures and the
// stage rows, computed off the request path.
type statusSnap struct {
	cov         wire.Coverage
	stageCounts map[string]int
	heldSince   []int64 // since_ns of every held Conversation
	at          time.Time
	warming     bool // no real figures yet
	// statExceptions is how many files the pass Lstat'ed.
	statExceptions int
}

// crashWindow is the window of status's crashed_recent and suspected_recent.
const crashWindow = 7 * 24 * time.Hour

// refreshStatus recomputes the snapshot from the maintained lag state and the
// cached file rows, with a Lstat only for the exceptions (see statLazy).
func (d *Daemon) refreshStatus() error { return d.refreshStatusCtx(d.rootCtx()) }

// refreshStatusCtx is refreshStatus with the coverage pass under ctx.
func (d *Daemon) refreshStatusCtx(ctx context.Context) error {
	d.statusRefreshMu.Lock()
	defer d.statusRefreshMu.Unlock()
	now := d.clock.Now()
	// The stage rows first: the coverage pass below reuses them from the cache.
	rows, err := d.loadStages()
	if err != nil {
		return err
	}
	cov, err := d.computeCoverageModeCtx(ctx, "summary", false, statLazy)
	if err != nil {
		return err
	}
	snap := &statusSnap{cov: cov, stageCounts: map[string]int{}, at: now, statExceptions: int(d.statExceptions.Load())}
	for _, r := range rows {
		snap.stageCounts[r.Stage]++
		if r.Stage == store.StageHeld {
			snap.heldSince = append(snap.heldSince, r.SinceNS)
		}
	}
	d.statusMu.Lock()
	d.statusSnap = snap
	d.statusMu.Unlock()
	d.statusFirst.Do(func() { close(d.statusReady) })
	return nil
}

// refreshCrashCounts recomputes the 7-day crashed and suspected counts from the
// model the engine keeps, not from a conversation.list read, so they cost one
// pass over the Conversations. It is a separate step from refreshStatus because
// it may load the model; a failure keeps the previous counts.
func (d *Daemon) refreshCrashCounts() error {
	c, su, err := d.eng.CountEnded(d.rootCtx(), d.clock.Now().Add(-crashWindow))
	if err != nil {
		return err
	}
	d.statusMu.Lock()
	d.crashed, d.suspected = &c, &su
	d.statusMu.Unlock()
	return nil
}

// RefreshStatus recomputes the cached status figures and the crash counts now,
// instead of at the next pass of the status loop.
func (d *Daemon) RefreshStatus() error {
	if err := d.refreshStatus(); err != nil {
		return err
	}
	return d.refreshCrashCounts()
}

func (d *Daemon) crashCounts() (crashed, suspected *int) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	return d.crashed, d.suspected
}

func (d *Daemon) loadStatusSnap() *statusSnap {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	return d.statusSnap
}

// statusSnapshot returns the cached figures. With the loop running and no
// snapshot yet it waits briefly, then answers a warming snapshot; without the
// loop (a daemon that was opened but not started) it computes once inline.
func (d *Daemon) statusSnapshot(ctx context.Context) (*statusSnap, error) {
	if s := d.loadStatusSnap(); s != nil {
		return s, nil
	}
	if !d.statusLoopOn.Load() {
		if err := d.refreshStatusCtx(ctx); err != nil {
			return nil, err
		}
		return d.loadStatusSnap(), nil
	}
	select {
	case <-d.statusReady:
	case <-time.After(statusFirstWait):
	}
	if s := d.loadStatusSnap(); s != nil {
		return s, nil
	}
	return &statusSnap{stageCounts: map[string]int{}, warming: true}, nil
}

func (d *Daemon) statusLoop() {
	for {
		if err := d.runLoop(obs.LoopStatusRefresh, d.refreshStatus); err != nil {
			d.obs.Event("status_refresh_failed", "error", err.Error())
		}
		if err := d.refreshCrashCounts(); err != nil {
			d.obs.Event("crash_counts_failed", "error", err.Error())
		}
		wait, stopTimer := d.timer(obs.LoopStatusRefresh, statusRefreshEvery)
		select {
		case <-d.stop:
			stopTimer()
			return
		case <-wait:
		}
	}
}
