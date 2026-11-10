package serve

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// walTruncateEvery is the least time between two TRUNCATE attempts, whatever
// their outcome: a TRUNCATE blocks writers while it waits for readers.
const walTruncateEvery = time.Hour

// walTruncateBusy is how long a TRUNCATE waits for readers before it gives up
// with busy; it bounds how long a writer can be held.
const walTruncateBusy = time.Second

// walCheckpointEvery is the passive checkpoint cadence (a constant in phase 0).
const walCheckpointEvery = time.Minute

// walCheckpointWarnEvery bounds the WARN a failing checkpoint logs.
const walCheckpointWarnEvery = time.Minute

// walCheckpointLoop runs a passive WAL checkpoint every walCheckpointEvery, then
// a guarded TRUNCATE attempt, until d.stop closes. Daemon.Close waits for it
// (goLoop) before the store closes; a checkpoint already running is not
// cancelled.
func (d *Daemon) walCheckpointLoop() {
	tick, stopTicker := d.ticker(obs.LoopWALCheckpoint, walCheckpointEvery)
	defer stopTicker()
	tickLoop(d.stop, tick, func() {
		d.checkpointOnce(d.passiveCheckpoint())
		d.maybeTruncate()
	})
}

// passiveCheckpoint is the Checkpoint seam: the func the loop runs for a passive
// checkpoint (the store's by default; a test sets d.checkpoint).
func (d *Daemon) passiveCheckpoint() func() (int, int, error) {
	if d.checkpoint != nil {
		return d.checkpoint
	}
	return d.st.Checkpoint
}

// truncateCheckpoint is the TRUNCATE seam (the store's by default; a test sets
// d.truncate).
func (d *Daemon) truncateCheckpoint() func(time.Duration) (bool, error) {
	if d.truncate != nil {
		return d.truncate
	}
	return d.st.CheckpointTruncate
}

// maybeTruncate attempts one TRUNCATE checkpoint when every guard holds:
// wal.truncate_above is set and the WAL file is over it, the startup backfill
// has finished (caught_up == 1), at least walTruncateEvery has passed since the
// last attempt (of any outcome), and a FRESH passive checkpoint taken just now
// reached every frame. The all-frames guard is what keeps the TRUNCATE from
// stalling writers: with a reader pinning frames the TRUNCATE would hold the
// write lock for its whole busy wait and still not finish (see
// TestTruncateWithoutAllFramesStallsWriters). busy is accepted: nothing is
// lost, the next hourly attempt retries.
func (d *Daemon) maybeTruncate() {
	limit := d.cfg.WALTruncateAbove
	if limit <= 0 || !d.backfillDone.Load() {
		return
	}
	now := d.clock.Now()
	if !d.lastTruncate.IsZero() && now.Sub(d.lastTruncate) < walTruncateEvery {
		return
	}
	fi, err := os.Stat(filepath.Join(d.cfg.StateDir, store.DBName+"-wal"))
	if err != nil || fi.Size() <= limit {
		return
	}
	log, done, err := d.passiveCheckpoint()()
	if err != nil || log != done {
		return
	}
	d.lastTruncate = now
	d.obs.Event(obs.EvWALTruncateStart, "source", "periodic")
	busy, terr := d.truncateCheckpoint()(walTruncateBusy)
	dur := d.clock.Now().Sub(now)
	outcome := "ok"
	switch {
	case terr != nil:
		outcome = "error"
		d.obs.Log.Warn("wal_truncate_failed", "err", terr.Error())
		d.noteNoSpace(obs.OpIndexCheckpoint, terr)
	case busy:
		outcome = "busy"
	}
	reg := d.obs.Reg
	reg.Add(obs.MWALTruncateTotal, map[string]string{"outcome": outcome}, 1)
	reg.Observe(obs.MWALTruncateDuration, dur.Seconds())
	d.obs.Event(obs.EvWALTruncateDone, "source", "periodic", "outcome", outcome,
		"duration_ms", strconv.FormatInt(dur.Milliseconds(), 10))
	if outcome == "ok" {
		var wal float64
		if fi, err := os.Stat(filepath.Join(d.cfg.StateDir, store.DBName+"-wal")); err == nil {
			wal = float64(fi.Size())
		}
		reg.Set(obs.MWALBytes, nil, wal)
	}
}

// tickLoop runs step on every tick until stop closes; no step starts once stop
// is closed.
func tickLoop(stop <-chan struct{}, tick <-chan time.Time, step func()) {
	for {
		select {
		case <-stop:
			return
		case <-tick:
			select {
			case <-stop:
				return
			default:
			}
			step()
		}
	}
}

// checkpointOnce runs one checkpoint through cp and records its duration, its
// outcome and the frame gauges. An error is outcome=error and is logged at WARN
// at most once a minute.
func (d *Daemon) checkpointOnce(cp func() (int, int, error)) {
	start := d.clock.Now()
	var log, done int
	err := d.runLoop(obs.LoopWALCheckpoint, func() error {
		var e error
		log, done, e = cp()
		return e
	})
	end := d.clock.Now()
	reg := d.obs.Reg
	if err != nil {
		if d.lastCkptWarn.IsZero() || end.Sub(d.lastCkptWarn) >= walCheckpointWarnEvery {
			d.lastCkptWarn = end
			d.obs.Log.Warn("wal_checkpoint_failed", "err", err.Error(), "locked", strings.Contains(err.Error(), "locked"))
		}
		d.noteNoSpace(obs.OpIndexCheckpoint, err)
	} else {
		reg.Set(obs.MWALFrames, nil, float64(log))
		reg.Set(obs.MWALCheckpointedFrames, nil, float64(done))
		reg.Set(obs.MWALCheckpointLastTime, nil, float64(end.UnixNano())/1e9)
	}
	reg.Observe(obs.MWALCheckpointDuration, end.Sub(start).Seconds())
}
