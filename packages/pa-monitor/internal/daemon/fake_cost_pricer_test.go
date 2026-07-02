package daemon

import (
	"context"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// fakeCostPricer is a poller.CostPricer stand-in for daemon integration tests:
// it returns a preset active block and a "probed, no error" state with no
// transcript scan. It replaces the retired exec-based usage.Provider that older
// integration tests fed a JSON body (Phase 4 native CostPricer switch).
type fakeCostPricer struct {
	block *usage.Block
}

func (f *fakeCostPricer) ActiveBlock(context.Context) (*usage.Block, error) {
	return f.block, nil
}

func (f *fakeCostPricer) Probed() (bool, error) { return true, nil }
