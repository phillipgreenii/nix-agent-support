package serve

import (
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
}

// statusSweepEvery is how often the status figures are recomputed over every
// file. Between two sweeps only the files that were lagging at the last sweep
// (the hot set) are re-read, so the loop's cost follows the number of lagging
// files, not the size of the tree. A file that starts to lag between sweeps is
// seen at the next sweep; its lag is measured from its modification time, so
// the figure is the same, only noticed up to this long later (well under the
// 60 s "behind" threshold).
const statusSweepEvery = 30 * time.Second

// refreshStatus recomputes the snapshot. Every statusSweepEvery it reads every
// file once (Lstat per file); in between it re-reads only the hot set.
func (d *Daemon) refreshStatus() error {
	d.statusRefreshMu.Lock()
	defer d.statusRefreshMu.Unlock()
	now := d.clock.Now()
	var cov wire.Coverage
	if prev := d.loadStatusSnap(); prev == nil || now.Sub(d.statusSweepAt) >= statusSweepEvery || now.Before(d.statusSweepAt) {
		full, err := d.computeCoverage("files", false)
		if err != nil {
			return err
		}
		d.statusHot = d.statusHot[:0]
		for _, fc := range full.FileList {
			if fc.CopyLagBytes > 0 || fc.ParseLagBytes > 0 {
				d.statusHot = append(d.statusHot, fc.FileID)
			}
		}
		full.FileList = nil // the status figures do not read the list
		cov = full
		d.statusSweepAt = now
	} else {
		cov = prev.cov
		var lagS, parseS float64
		err := d.st.ReadTx(func(rd *store.Reader) error {
			var e error
			d.statusHot, lagS, parseS, e = d.hotLag(rd, d.statusHot, now)
			return e
		})
		if err != nil {
			return err
		}
		cov.CopyLagSecondsMax, cov.ParseLagSecondsMax = lagS, parseS
		cov.CaughtUp = d.backfillDone.Load()
		cov.Degraded = !cov.CaughtUp
		cov.Backfill = d.backfillProgress()
	}
	snap := &statusSnap{cov: cov, stageCounts: map[string]int{}, at: now}
	rows, err := d.st.Stages()
	if err != nil {
		return err
	}
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

func (d *Daemon) loadStatusSnap() *statusSnap {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	return d.statusSnap
}

// statusSnapshot returns the cached figures. With the loop running and no
// snapshot yet it waits briefly, then answers a warming snapshot; without the
// loop (a daemon that was opened but not started) it computes once inline.
func (d *Daemon) statusSnapshot() (*statusSnap, error) {
	if s := d.loadStatusSnap(); s != nil {
		return s, nil
	}
	if !d.statusLoopOn.Load() {
		if err := d.refreshStatus(); err != nil {
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
	defer d.refreshing.Done()
	for {
		if err := d.runLoop(obs.LoopStatusRefresh, d.refreshStatus); err != nil {
			d.obs.Event("status_refresh_failed", "error", err.Error())
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

// hotLag re-reads the given files and returns those still lagging (the next hot
// set) with the largest copy and parse lag in seconds among them.
func (d *Daemon) hotLag(rd *store.Reader, ids []int64, now time.Time) (hot []int64, copyS, parseS float64, err error) {
	if len(ids) == 0 {
		return ids, 0, 0, nil
	}
	files, err := rd.FilesByIDs(ids)
	if err != nil {
		return ids, 0, 0, err
	}
	paths, err := rd.PathsByFiles(ids)
	if err != nil {
		return ids, 0, 0, err
	}
	hot = make([]int64, 0, len(ids))
	for _, f := range files {
		ps := paths[f.ID]
		if f.State == "deleted" || len(ps) == 0 {
			continue
		}
		var copyOff, parseOff int64
		if g, gerr := rd.Generation(f.ID, f.CurGen); gerr == nil {
			copyOff, parseOff = g.CopyOffset, g.ParseOffset
		}
		fc := d.fileCoverage(f, ps[0], copyOff, parseOff, now)
		if fc.CopyLagBytes <= 0 && fc.ParseLagBytes <= 0 {
			continue
		}
		hot = append(hot, f.ID)
		copyS = max(copyS, fc.CopyLagS)
		if fc.ParseLagBytes > 0 {
			parseS = max(parseS, parseLagSeconds(rd, f.ID, f.CurGen, fc.ParseOffset, now))
		}
	}
	return hot, copyS, parseS, nil
}
