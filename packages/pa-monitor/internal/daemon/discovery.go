package daemon

import (
	"context"
	"syscall"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
)

// pidAlive reports whether pid is a live process, using kill(pid, 0)
// semantics: no signal is actually delivered, but the syscall's error tells
// us whether the process exists. EPERM means the process exists but is owned
// by another user (or has changed privileges) — still alive, so it must not
// be reaped. ESRCH (or any other error) means no such process — dead. This is
// RunReaper's default isAlive check for pruning dead bridge members.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

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
