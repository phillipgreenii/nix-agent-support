// Package block tracks 5-hour billing block transitions. Folds ccusage
// block snapshots into a stable correlation ID (block.id = UTC hour of
// block start) and fires a limit-hit callback at most once per block.
package block

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
)

type Tracker struct {
	capUSD     float64
	currentID  string
	hitFired   bool
	OnLimitHit func()
}

func NewTracker(capUSD float64) *Tracker {
	return &Tracker{capUSD: capUSD}
}

// ID returns the current block.id label value, or "" if no block has
// been observed yet.
func (t *Tracker) ID() string { return t.currentID }

// Update folds a fresh ccusage block snapshot into the tracker. Fires
// OnLimitHit at most once per block.
func (t *Tracker) Update(b *ccusage.Block) {
	if b == nil {
		return
	}
	id := b.StartTime.UTC().Format("2006-01-02T15Z")
	if id != t.currentID {
		t.currentID = id
		t.hitFired = false
	}
	if !t.hitFired && t.capUSD > 0 && b.CostUSD >= t.capUSD {
		t.hitFired = true
		if t.OnLimitHit != nil {
			t.OnLimitHit()
		}
	}
}
