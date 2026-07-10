// Package block tracks 5-hour billing block transitions. Folds block
// snapshots into a stable correlation ID (block.id = UTC hour of block
// start) used as a metric label.
//
// The cost-cap limit-hit trigger this tracker once fired (ccusage native
// cost >= dollar cap) is RETIRED (ADR 0024 D3): ccusage cost is not accurate
// enough. The account-level limit-hit is now detected from the authoritative
// FiveHourPct / terminal usage-limit signal in the daemon tick loop. This
// tracker is retained solely for the block.id correlation.
package block

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

type Tracker struct {
	currentID string
}

func NewTracker() *Tracker { return &Tracker{} }

// ID returns the current block.id label value, or "" if no block has
// been observed yet.
func (t *Tracker) ID() string { return t.currentID }

// Update folds a fresh block snapshot into the tracker, advancing the
// block.id correlation. A nil block is a no-op.
func (t *Tracker) Update(b *usage.Block) {
	if b == nil {
		return
	}
	t.currentID = b.StartTime.UTC().Format("2006-01-02T15Z")
}
