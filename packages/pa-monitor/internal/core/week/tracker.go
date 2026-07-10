// Package week tracks Anthropic's weekly limit via weekly usage data.
// Folds the current week's entry into an ISO-week correlation ID used as a
// metric label.
//
// Note: weeks are grouped by Monday-anchor local time. Anthropic's actual
// reset boundary is not authoritative here; switch to anchor-relative IDs
// if/when the discrepancy proves material.
//
// The cost-cap limit-hit trigger this tracker once fired is RETIRED (ADR 0024
// D3): the account-level weekly limit-hit is now detected from the
// authoritative SevenDayPct signal in the daemon tick loop. This tracker is
// retained solely for the week.id correlation.
package week

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

type Tracker struct {
	currentID string
}

func NewTracker() *Tracker { return &Tracker{} }

func (t *Tracker) ID() string { return t.currentID }

// Update folds a fresh weekly entry into the tracker, advancing the week.id
// correlation. A nil entry or an unparseable period is a no-op.
func (t *Tracker) Update(e *usage.WeeklyEntry) {
	if e == nil {
		return
	}
	d, err := time.Parse("2006-01-02", e.Period)
	if err != nil {
		return
	}
	y, w := d.ISOWeek()
	t.currentID = fmt.Sprintf("%d-W%02d", y, w)
}
