package week

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/ccusage"
)

func TestTracker_IDFromISOWeek(t *testing.T) {
	tr := NewTracker(500.0)
	tr.Update(&ccusage.WeeklyEntry{Period: "2026-05-18", TotalCost: 50.0})
	if got := tr.ID(); got != "2026-W21" {
		t.Errorf("ID = %q", got)
	}
}

func TestTracker_LimitHitFiresOnce(t *testing.T) {
	tr := NewTracker(100.0)
	hits := 0
	tr.OnLimitHit = func() { hits++ }
	tr.Update(&ccusage.WeeklyEntry{Period: "2026-05-18", TotalCost: 50.0})
	if hits != 0 {
		t.Error("under-cap")
	}
	tr.Update(&ccusage.WeeklyEntry{Period: "2026-05-18", TotalCost: 120.0})
	if hits != 1 {
		t.Error("first over-cap")
	}
	tr.Update(&ccusage.WeeklyEntry{Period: "2026-05-18", TotalCost: 130.0})
	if hits != 1 {
		t.Error("dup hit")
	}
}

func TestTracker_NewWeekResetsHit(t *testing.T) {
	tr := NewTracker(100.0)
	tr.Update(&ccusage.WeeklyEntry{Period: "2026-05-18", TotalCost: 120.0})
	hits := 0
	tr.OnLimitHit = func() { hits++ }
	tr.Update(&ccusage.WeeklyEntry{Period: "2026-05-25", TotalCost: 120.0})
	if hits != 1 {
		t.Errorf("new week should re-hit, got %d", hits)
	}
}

func TestTracker_BadPeriodIsIgnored(t *testing.T) {
	tr := NewTracker(100.0)
	tr.Update(&ccusage.WeeklyEntry{Period: "garbage", TotalCost: 999.0})
	if tr.ID() != "" {
		t.Errorf("expected empty ID after bad period")
	}
}

func TestTracker_NilIsNoop(t *testing.T) {
	tr := NewTracker(100.0)
	tr.Update(nil)
	if tr.ID() != "" {
		t.Errorf("expected empty ID after nil update")
	}
}
