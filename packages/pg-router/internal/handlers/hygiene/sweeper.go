package hygiene

import (
	"context"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
)

// Sweeper owns the handler's reclaim and teardown passes: the janitor, the
// per-role orphan unit, the capacity-pressure reconcile, the worktree sweeps
// and the purge retry. It holds the ports the passes need from the command
// that embeds it, so the passes carry no knowledge of the command's own
// state.
type Sweeper struct {
	// Version is the handler binary's stamped version (the build_info gauge).
	Version string
	// Help is the usage text printed for -h.
	Help string
}

// InstallProductionGauges publishes the orphan last-event gauge through the
// ccpool OTel emitter; the handler binary calls it once at start.
func (Sweeper) InstallProductionGauges() { orphanGaugeEmit = ccpoolGaugeEmit }

// DisableTeardownProbe makes the closed-bead teardown see no live process, so a
// test of the embedding command does not depend on lsof. Test use only.
func DisableTeardownProbe() {
	teardownProbe = func(context.Context) ([]string, error) { return nil, nil }
}

// ReconcileDefaultPool runs the guarded default-pool stale-session pass and
// returns how many sessions it closed.
func (Sweeper) ReconcileDefaultPool(ctx context.Context, cfg config.Config) int {
	return reconcileDefaultPool(ctx, cfg)
}
