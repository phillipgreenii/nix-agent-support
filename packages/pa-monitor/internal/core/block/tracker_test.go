package block

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

func TestTracker_IDDerivedFromBlockStart(t *testing.T) {
	b := &usage.Block{
		StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC),
		CostUSD:   3.5,
	}
	tr := NewTracker()
	tr.Update(b)
	if got := tr.ID(); got != "2026-05-20T14Z" {
		t.Errorf("ID = %q, want 2026-05-20T14Z", got)
	}
}

func TestTracker_NewBlockAdvancesID(t *testing.T) {
	tr := NewTracker()
	tr.Update(&usage.Block{StartTime: time.Date(2026, 5, 20, 14, 0, 0, 0, time.UTC)})
	tr.Update(&usage.Block{StartTime: time.Date(2026, 5, 20, 19, 0, 0, 0, time.UTC)})
	if got := tr.ID(); got != "2026-05-20T19Z" {
		t.Errorf("ID = %q, want 2026-05-20T19Z (new block advances the correlation)", got)
	}
}

func TestTracker_NilBlockIsNoop(t *testing.T) {
	tr := NewTracker()
	tr.Update(nil)
	if tr.ID() != "" {
		t.Errorf("ID should remain empty after nil update")
	}
}
