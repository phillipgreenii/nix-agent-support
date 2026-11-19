package hygiene

import (
	"context"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
)

// Sweeper owns the handler's reclaim and teardown passes: the janitor, the
// per-role orphan unit, the capacity-pressure reconcile and the purge retry. It holds the ports the passes need from the command
// that embeds it, so the passes carry no knowledge of the command's own
// state.
type Sweeper struct {
	// Version is the handler binary's stamped version (the build_info gauge).
	Version string
	// Help is the usage text printed for -h.
	Help string

	// The fields below are ports and test seams; each zero value selects the
	// production behavior, so a command embeds a Sweeper without setting them.

	// orphanGaugeEmit publishes the time of the latest orphan event per role and
	// kind; nil publishes nothing (set by InstallProductionGauges).
	orphanGaugeEmit gaugeEmitFn
	// listDiagExpiry is when the slow-list diagnostic stops by itself (default
	// defaultListDiagExpiry).
	listDiagExpiry time.Time
	// ports are the teardown and bulk-delete ports; the zero value is
	// production.
	ports teardownPorts
}

// diagExpiry is when the slow-list diagnostic stops by itself.
func (s Sweeper) diagExpiry() time.Time {
	if !s.listDiagExpiry.IsZero() {
		return s.listDiagExpiry
	}
	return defaultListDiagExpiry()
}

// env stamps the Sweeper's gauge, sweep bounds and teardown ports onto an
// orphanEnv, leaving every field the caller set (open, quiet, cwds, lockDir)
// alone.
func (s Sweeper) env(e orphanEnv) orphanEnv {
	e.gauge = s.orphanGaugeEmit
	e.ports = s.ports
	return e
}

// InstallProductionGauges publishes the orphan last-event gauge through the
// ccpool OTel emitter; the handler binary calls it once at start.
func (s *Sweeper) InstallProductionGauges() { s.orphanGaugeEmit = ccpoolGaugeEmit }

// ReconcileDefaultPool runs the guarded default-pool stale-session pass and
// returns how many sessions it closed.
func (s Sweeper) ReconcileDefaultPool(ctx context.Context, cfg config.Config) int {
	return s.ports.reconcileDefaultPool(ctx, cfg)
}
