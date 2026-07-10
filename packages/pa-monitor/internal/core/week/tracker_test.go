package week

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

func TestTracker_IDFromISOWeek(t *testing.T) {
	tr := NewTracker()
	tr.Update(&usage.WeeklyEntry{Period: "2026-05-18", TotalCost: 50.0})
	if got := tr.ID(); got != "2026-W21" {
		t.Errorf("ID = %q", got)
	}
}

func TestTracker_NewWeekAdvancesID(t *testing.T) {
	tr := NewTracker()
	tr.Update(&usage.WeeklyEntry{Period: "2026-05-18", TotalCost: 120.0})
	tr.Update(&usage.WeeklyEntry{Period: "2026-05-25", TotalCost: 120.0})
	if got := tr.ID(); got != "2026-W22" {
		t.Errorf("ID = %q, want 2026-W22 (new week advances the correlation)", got)
	}
}

func TestTracker_BadPeriodIsIgnored(t *testing.T) {
	tr := NewTracker()
	tr.Update(&usage.WeeklyEntry{Period: "garbage", TotalCost: 999.0})
	if tr.ID() != "" {
		t.Errorf("expected empty ID after bad period")
	}
}

func TestTracker_NilIsNoop(t *testing.T) {
	tr := NewTracker()
	tr.Update(nil)
	if tr.ID() != "" {
		t.Errorf("expected empty ID after nil update")
	}
}
