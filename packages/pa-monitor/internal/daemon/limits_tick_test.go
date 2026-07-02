package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
)

// TestApplyLimits_PopulatesTree proves a LimitsSource reading is copied onto the
// tree's account-global rate_limits fields, with absent fields left unknown.
func TestApplyLimits_PopulatesTree(t *testing.T) {
	tree := &aggregate.Tree{}
	fivePct := 34.0
	fiveRst := time.Unix(1782958200, 0)
	capAt := time.Unix(1700000000, 0)
	applyLimits(tree, &Limits{
		FiveHourPct:      &fivePct,
		FiveHourResetsAt: fiveRst,
		CapturedAt:       capAt,
		// SevenDay* unset -> must stay unknown.
	})

	if tree.FiveHourPct == nil || *tree.FiveHourPct != 34 {
		t.Errorf("FiveHourPct = %v, want 34", tree.FiveHourPct)
	}
	if !tree.FiveHourResetsAt.Equal(fiveRst) {
		t.Errorf("FiveHourResetsAt = %v, want %v", tree.FiveHourResetsAt, fiveRst)
	}
	if !tree.LimitsCapturedAt.Equal(capAt) {
		t.Errorf("LimitsCapturedAt = %v, want %v", tree.LimitsCapturedAt, capAt)
	}
	if tree.SevenDayPct != nil {
		t.Errorf("SevenDayPct = %v, want nil (absent)", *tree.SevenDayPct)
	}
	if !tree.SevenDayResetsAt.IsZero() {
		t.Errorf("SevenDayResetsAt = %v, want zero (absent)", tree.SevenDayResetsAt)
	}
}

// TestApplyLimits_NilReadingLeavesTreeUntouched: a nil reading (no data yet) must
// not clobber whatever the tree already holds from the persisted block.
func TestApplyLimits_NilReadingLeavesTreeUntouched(t *testing.T) {
	prior := 12.0
	tree := &aggregate.Tree{FiveHourPct: &prior}
	applyLimits(tree, nil)
	if tree.FiveHourPct == nil || *tree.FiveHourPct != 12 {
		t.Errorf("FiveHourPct = %v, want 12 preserved (nil reading must not clobber)", tree.FiveHourPct)
	}
}

// TestBlockToStoreBlock_CarriesTreeLimits proves the tree's rate_limits are
// persisted onto the store.Block so the store->tree (GetState) path reflects them.
func TestBlockToStoreBlock_CarriesTreeLimits(t *testing.T) {
	fivePct := 34.0
	sevPct := 5.0
	fiveRst := time.Unix(1782958200, 0)
	sevRst := time.Unix(1783000000, 0)
	capAt := time.Unix(1700000000, 0)
	tree := &aggregate.Tree{
		FiveHourPct:      &fivePct,
		SevenDayPct:      &sevPct,
		FiveHourResetsAt: fiveRst,
		SevenDayResetsAt: sevRst,
		LimitsCapturedAt: capAt,
	}
	b := &ccusage.Block{ID: "blk", CostUSD: 1.0}
	sb := blockToStoreBlockWithLimits(b, 90.0, time.Now().UTC(), tree)

	if sb.FiveHourPct == nil || *sb.FiveHourPct != 34 {
		t.Errorf("store FiveHourPct = %v, want 34", sb.FiveHourPct)
	}
	if sb.SevenDayPct == nil || *sb.SevenDayPct != 5 {
		t.Errorf("store SevenDayPct = %v, want 5", sb.SevenDayPct)
	}
	if sb.FiveHourResetsAt == nil || !sb.FiveHourResetsAt.Equal(fiveRst) {
		t.Errorf("store FiveHourResetsAt = %v, want %v", sb.FiveHourResetsAt, fiveRst)
	}
	if sb.SevenDayResetsAt == nil || !sb.SevenDayResetsAt.Equal(sevRst) {
		t.Errorf("store SevenDayResetsAt = %v, want %v", sb.SevenDayResetsAt, sevRst)
	}
	if sb.LimitsCapturedAt == nil || !sb.LimitsCapturedAt.Equal(capAt) {
		t.Errorf("store LimitsCapturedAt = %v, want %v", sb.LimitsCapturedAt, capAt)
	}
}

// TestBlockToStoreBlock_UnknownLimitsStayNil: an unknown tree limit persists as a
// nil pointer (unknown/stale), never a 0 or a 1970 timestamp.
func TestBlockToStoreBlock_UnknownLimitsStayNil(t *testing.T) {
	tree := &aggregate.Tree{} // all rate_limits unknown
	b := &ccusage.Block{ID: "blk"}
	sb := blockToStoreBlockWithLimits(b, 90.0, time.Now().UTC(), tree)
	if sb.FiveHourPct != nil {
		t.Errorf("FiveHourPct = %v, want nil", *sb.FiveHourPct)
	}
	if sb.FiveHourResetsAt != nil {
		t.Errorf("FiveHourResetsAt = %v, want nil (never 1970)", *sb.FiveHourResetsAt)
	}
	if sb.SevenDayResetsAt != nil {
		t.Errorf("SevenDayResetsAt = %v, want nil (never 1970)", *sb.SevenDayResetsAt)
	}
	if sb.LimitsCapturedAt != nil {
		t.Errorf("LimitsCapturedAt = %v, want nil", *sb.LimitsCapturedAt)
	}
}
