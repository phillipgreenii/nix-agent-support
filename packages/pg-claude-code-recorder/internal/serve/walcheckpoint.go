package serve

import (
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// walCheckpointEvery is the passive checkpoint cadence (a constant in phase 0).
const walCheckpointEvery = time.Minute

// walCheckpointWarnEvery bounds the WARN a failing checkpoint logs.
const walCheckpointWarnEvery = time.Minute

// walCheckpointLoop runs a passive WAL checkpoint every walCheckpointEvery until
// d.stop closes. Daemon.Close waits for it (d.refreshing) before the store
// closes; a checkpoint already running is not cancelled.
func (d *Daemon) walCheckpointLoop() {
	defer d.refreshing.Done()
	t := time.NewTicker(walCheckpointEvery)
	defer t.Stop()
	tickLoop(d.stop, t.C, func() { d.checkpointOnce(d.st.Checkpoint) })
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
	log, done, err := cp()
	end := d.clock.Now()
	reg := d.obs.Reg
	outcome := obs.OutcomeOK
	if err != nil {
		outcome = obs.OutcomeError
		if d.lastCkptWarn.IsZero() || end.Sub(d.lastCkptWarn) >= walCheckpointWarnEvery {
			d.lastCkptWarn = end
			d.obs.Log.Warn("wal_checkpoint_failed", "err", err.Error(), "locked", strings.Contains(err.Error(), "locked"))
		}
	} else {
		reg.Set(obs.MWALFrames, nil, float64(log))
		reg.Set(obs.MWALCheckpointedFrames, nil, float64(done))
		reg.Set(obs.MWALCheckpointLastTime, nil, float64(end.UnixNano())/1e9)
	}
	reg.Observe(obs.MWALCheckpointDuration, end.Sub(start).Seconds())
	reg.Add(obs.MLoopRuns, map[string]string{"loop": obs.LoopWALCheckpoint, "outcome": outcome}, 1)
}
