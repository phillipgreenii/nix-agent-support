package daemon

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// surfaceResolver answers the nudger's per-pid questions (HasSurface,
// HasUnsubmittedInput) for one tick. It is built from the tick's tree and
// resolves each pid through the terminal host the poller already cached on the
// session (provider.Cache.TerminalHost), so a cmux-hosted session never pays
// the tmux ancestry walk, and a cached cmux host costs no walk at all
// (pg2-lcohce; spike pg2-57blt measured 231 -> about 63 ps execs per tick at
// 22 sessions). A pid with no usable host falls back to the full first-match
// walk.
type surfaceResolver struct {
	signalers []signal.Signaler
	hostByPID map[int]string // live sessions only; "" marks an ambiguous pid
}

// newSurfaceResolver builds the per-tick resolver over the FULL signaler set
// (Detect-only; never Send, so the daemon never execs cmux, ADR 0022).
//
// The index holds only sessions whose pid is alive: a dead-not-GC'd session
// keeps its frozen host, and a reused pid must never inherit it. Two live
// sessions claiming one pid cannot both be right, so the pid is marked
// ambiguous and resolves through the full walk.
func newSurfaceResolver(signalers []signal.Signaler, tree *aggregate.Tree) *surfaceResolver {
	r := &surfaceResolver{signalers: signalers, hostByPID: map[int]string{}}
	if tree == nil {
		return r
	}
	for _, sv := range tree.Sessions() {
		if sv.Session == nil || !sv.PidAlive {
			continue
		}
		host := signal.BareHost(sv.TerminalHost)
		if prev, seen := r.hostByPID[sv.PID]; seen && prev != host {
			host = ""
		}
		r.hostByPID[sv.PID] = host
	}
	return r
}

// HasSurface reports whether pid has a deliverable terminal surface.
func (r *surfaceResolver) HasSurface(pid int) bool {
	return signal.ResolveSignalerForHost(r.signalers, r.hostByPID[pid], pid) != nil
}

// HasUnsubmittedInput reports whether pid's surface shows composed-but-not-
// submitted input (fail-closed; see signal.HasUnsubmittedInput).
func (r *surfaceResolver) HasUnsubmittedInput(pid int) bool {
	return signal.HasUnsubmittedInputForHost(r.signalers, r.hostByPID[pid], pid)
}
