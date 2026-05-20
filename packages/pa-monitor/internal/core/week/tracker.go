// Package week tracks Anthropic's weekly limit via ccusage weekly data.
// Folds the current week's entry into an ISO-week correlation ID and
// fires a limit-hit callback at most once per week.
//
// Note: ccusage groups weeks by Monday-anchor local time. Anthropic's
// actual reset boundary is not authoritative here; switch to anchor-
// relative IDs if/when the discrepancy proves material.
package week

import (
	"fmt"
	"time"

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

func (t *Tracker) ID() string { return t.currentID }

func (t *Tracker) Update(e *ccusage.WeeklyEntry) {
	if e == nil {
		return
	}
	d, err := time.Parse("2006-01-02", e.Period)
	if err != nil {
		return
	}
	y, w := d.ISOWeek()
	id := fmt.Sprintf("%d-W%02d", y, w)
	if id != t.currentID {
		t.currentID = id
		t.hitFired = false
	}
	if !t.hitFired && t.capUSD > 0 && e.TotalCost >= t.capUSD {
		t.hitFired = true
		if t.OnLimitHit != nil {
			t.OnLimitHit()
		}
	}
}
