package poller

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// TestMonitorLimitsWeekly_ReadPublishedDerivedState is the C5 rewire gate: the
// lifecycle reads limits/weekly through Poller.MonitorLimits / MonitorWeekly,
// which after Phase-3 read the PUBLISHED DerivedState (an atomic Load) rather
// than calling Monitor.Limits/Weekly directly — so the emit tick never touches
// the producer-owned Monitor.
func TestMonitorLimitsWeekly_ReadPublishedDerivedState(t *testing.T) {
	now := time.Unix(1_776_000_300, 0)
	p := &Poller{Now: func() time.Time { return now }}
	prod := p.Producer()

	wantLimits := &limits.Limits{}
	prod.Publish(&DerivedState{
		Limits: wantLimits,
		Weekly: &usage.WeeklyEntry{Period: "2026-07-20", TotalCost: 3.5},
	})

	if got := p.MonitorLimits(); got != wantLimits {
		t.Errorf("MonitorLimits() = %p, want the published DerivedState.Limits %p", got, wantLimits)
	}
	if w := p.MonitorWeekly(now); w == nil || w.TotalCost != 3.5 {
		t.Errorf("MonitorWeekly() = %+v, want the published DerivedState.Weekly (TotalCost 3.5)", w)
	}
}

// TestMonitorPRByDir_ReadPublishedDerivedState is the F1 delivery seam: the
// daemon reads the cwd->*PRInfo map through Poller.MonitorPRByDir, which Loads
// the PUBLISHED DerivedState.PRByDir (an atomic read of producer-owned state)
// rather than touching the producer's Monitor/providers. Nil before the first
// publish; the published map afterward.
func TestMonitorPRByDir_ReadPublishedDerivedState(t *testing.T) {
	p := &Poller{Now: func() time.Time { return time.Unix(1_776_000_300, 0) }}
	prod := p.Producer()

	if got := p.MonitorPRByDir(); got != nil {
		t.Errorf("MonitorPRByDir() before publish = %v, want nil", got)
	}

	want := map[string]*session.PRInfo{
		"/repo": {Number: 7, State: "OPEN", URL: "https://example.com/pull/7"},
	}
	prod.Publish(&DerivedState{PRByDir: want})

	got := p.MonitorPRByDir()
	if got["/repo"] == nil || got["/repo"].Number != 7 || got["/repo"].State != "OPEN" {
		t.Errorf("MonitorPRByDir()[/repo] = %+v, want {Number:7 State:OPEN ...}", got["/repo"])
	}
}
