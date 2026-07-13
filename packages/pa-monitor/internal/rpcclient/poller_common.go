package rpcclient

import "github.com/phillipgreenii/pa-monitor/internal/core/aggregate"

// anyWorking reports whether any directory in the tree has a working session.
// Shared by the pollers that satisfy tui.Poller.
func anyWorking(t *aggregate.Tree) bool {
	if t == nil {
		return false
	}
	for _, d := range t.Dirs {
		if d.WorkingN > 0 {
			return true
		}
	}
	return false
}

// ErrOffline is the sentinel a poller returns when it is serving cached state
// because the daemon is currently unreachable (dial/stream down, or inside a
// reconnect window). Callers distinguish "daemon-down, caching" from a real
// RPC error via errors.Is(err, ErrOffline).
type errOffline struct{}

func (errOffline) Error() string { return "pa-monitor daemon offline; serving cached state" }

// ErrOffline is the sentinel returned while a poller is offline.
var ErrOffline error = errOffline{}
