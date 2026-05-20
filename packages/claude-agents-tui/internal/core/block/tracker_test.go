package block

import (
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/ccusage"
)

func TestTracker_IDDerivedFromBlockStart(t *testing.T) {
	b := &ccusage.Block{
		StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC),
		CostUSD:   3.5,
	}
	tr := NewTracker(20.0)
	tr.Update(b)
	if got := tr.ID(); got != "2026-05-20T14Z" {
		t.Errorf("ID = %q, want 2026-05-20T14Z", got)
	}
}

func TestTracker_LimitHitTransitionFiresOnce(t *testing.T) {
	tr := NewTracker(10.0)
	hits := 0
	tr.OnLimitHit = func() { hits++ }

	tr.Update(&ccusage.Block{StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC), CostUSD: 5.0})
	if hits != 0 {
		t.Errorf("under-cap should not hit, got %d", hits)
	}
	tr.Update(&ccusage.Block{StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC), CostUSD: 12.0})
	if hits != 1 {
		t.Errorf("over-cap should hit once, got %d", hits)
	}
	tr.Update(&ccusage.Block{StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC), CostUSD: 15.0})
	if hits != 1 {
		t.Errorf("further updates in same block should not re-hit, got %d", hits)
	}
}

func TestTracker_NewBlockResetsLimitHitFlag(t *testing.T) {
	tr := NewTracker(10.0)
	tr.Update(&ccusage.Block{StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC), CostUSD: 12.0})
	hits := 0
	tr.OnLimitHit = func() { hits++ }
	tr.Update(&ccusage.Block{StartTime: time.Date(2026, 5, 20, 19, 0, 0, 0, time.UTC), CostUSD: 11.0})
	if hits != 1 {
		t.Errorf("new block should fire fresh hit, got %d", hits)
	}
}

func TestTracker_NilBlockIsNoop(t *testing.T) {
	tr := NewTracker(10.0)
	tr.Update(nil)
	if tr.ID() != "" {
		t.Errorf("ID should remain empty after nil update")
	}
}

func TestTracker_ZeroCapNeverHits(t *testing.T) {
	tr := NewTracker(0)
	hits := 0
	tr.OnLimitHit = func() { hits++ }
	tr.Update(&ccusage.Block{StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC), CostUSD: 999})
	if hits != 0 {
		t.Errorf("zero cap should never fire, got %d", hits)
	}
}
