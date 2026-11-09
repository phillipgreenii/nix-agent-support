//go:build integration

package daemon

import "github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"

// noopKeeper is a caffeinate.Keeper that does nothing, for tests that drive the
// tick loop but do not care about the keeper.
type noopKeeper struct{}

func (noopKeeper) Acquire(int) error               { return nil }
func (noopKeeper) Release() error                  { return nil }
func (noopKeeper) Observe() caffeinate.Observation { return caffeinate.Observation{} }
