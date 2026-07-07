package daemon

import (
	"context"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
)

// reapOnce runs a single reap pass over reg, dropping bridge members whose
// process is no longer alive according to isAlive. It is a thin wrapper
// around Registry.Prune so the periodic loop and its tests have a single,
// named unit of work independent of the RPC path.
func reapOnce(reg *bridge.Registry, isAlive func(pid int) bool) {
	reg.Prune(isAlive)
}

// RunReaper runs reapOnce on every tick of interval until ctx is cancelled,
// at which point it returns. It does not run reapOnce immediately on start;
// the first prune happens after the first tick elapses.
//
// Wiring RunReaper into daemon startup (RunWith) is a later task; this
// function is decoupled from any RPC path so it can be started independently
// once that wiring lands.
func RunReaper(ctx context.Context, reg *bridge.Registry, interval time.Duration, isAlive func(pid int) bool) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reapOnce(reg, isAlive)
		}
	}
}
