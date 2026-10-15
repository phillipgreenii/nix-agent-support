package keeper

import (
	"slices"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// backstopExes are the only executables the backstop will signal: the shim
// that stands in for caffeinate, and the CLI's run subcommand.
var backstopExes = []string{"pg-caffeinate-shim", "pg-keep-awake"}

// Backstop sends SIGTERM to the clients of expired or orphaned leases that are
// still alive and stuck, and returns the pids it signalled.
//
// It is the backstop for a shim or run client that outlives its lease. It MAY
// signal a client only when ALL of these hold, checked immediately before the
// signal:
//
//   - the reap reason is expired or orphan (a dead client or watch needs none);
//   - the lease kind is shim or run, never acquire (an acquire lease's pid may
//     be pa-monitor itself, or any caller);
//   - the lease records a client, and its pid is above 1 and is not the
//     keeper's own;
//   - that pid still exists, is not a zombie and still has the recorded
//     start_us (so a recycled pid is never signalled);
//   - its executable name is pg-caffeinate-shim or pg-keep-awake.
//
// Nothing read from a lease file is executed or interpolated.
func (s *Sweeper) Backstop(reaped []ReapedLease) []int {
	var signalled []int
	for _, r := range reaped {
		if r.Reason != ReapExpired && r.Reason != ReapOrphan {
			continue
		}
		l := r.Lease
		if l == nil || l.Client == nil || (l.Kind != lease.KindShim && l.Kind != lease.KindRun) {
			continue
		}
		pid := l.Client.PID
		if pid <= 1 || pid == s.opts.Self || slices.Contains(signalled, pid) {
			continue
		}
		info, err := s.opts.Info(pid)
		if err != nil || !slices.Contains(backstopExes, info.Name) {
			continue
		}
		// Probe last, right before the signal, to keep the pid-reuse window
		// as small as the API allows.
		st := s.opts.Prober.Probe(pid)
		if !st.Exists || st.Zombie || st.StartUS != l.Client.StartUS {
			continue
		}
		if err := s.opts.Signal(pid, syscall.SIGTERM); err == nil {
			signalled = append(signalled, pid)
		}
	}
	return signalled
}
