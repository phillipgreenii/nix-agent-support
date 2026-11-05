package serve

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// SweepRequest is the body of the sweep operation. Force empties the skip
// cache and the seen table and starts a sweep now that ingests every file;
// without it the operation only reports the state of the last forced sweep.
type SweepRequest struct {
	Force bool `json:"force,omitempty"`
}

// SweepAnswer is the answer of the sweep operation. Started is true when this
// request began a forced sweep; Running is true while one runs (a request made
// during a run starts nothing); Completed counts the forced sweeps that have
// finished since the daemon started; Error is the last one's failure, if any.
type SweepAnswer struct {
	Started   bool   `json:"started"`
	Running   bool   `json:"running"`
	Completed int64  `json:"completed"`
	Error     string `json:"error,omitempty"`
}

// opSweep starts a forced full sweep on request (the full verification that
// catches a rewrite which keeps size, mtime and inode). The sweep runs in the
// background; a caller that wants to wait polls this operation without Force.
func (d *Daemon) opSweep(_ context.Context, body []byte) (any, *wire.Error) {
	var req SweepRequest
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	d.forceMu.Lock()
	defer d.forceMu.Unlock()
	started := false
	if req.Force && !d.forceRunning {
		d.forceRunning, started = true, true
		d.forcePending.Store(true)
		d.refreshing.Add(1)
		go d.runForcedSweep()
	}
	return SweepAnswer{Started: started, Running: d.forceRunning, Completed: d.forceDone, Error: d.forceErr}, nil
}

// runForcedSweep is the background pass of a forced sweep: the watcher's
// OnSweep hook consumes forcePending and empties the caches under the
// watcher's lock, so no other sweep can re-fill them between the clear and
// this sweep's walk.
func (d *Daemon) runForcedSweep() {
	defer d.refreshing.Done()
	err := d.w.Rescan()
	d.forcePending.Store(false) // the sweep failed before its hook ran
	d.forceMu.Lock()
	d.forceRunning = false
	d.forceDone++
	d.forceErr = ""
	if err != nil {
		d.forceErr = err.Error()
	}
	d.forceMu.Unlock()
	if err != nil {
		d.obs.Log.Warn("forced_sweep_failed", "err", err.Error())
	} else {
		d.obs.Log.Info("forced_sweep_done")
	}
}
