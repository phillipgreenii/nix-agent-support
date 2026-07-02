package usage

import (
	"testing"
	"time"
)

// stdPrices mirrors Anthropic's published per-MTok pricing for the two models
// the parity fixtures use, so the hand-computed pinned figures below equal what
// ccusage/LiteLLM compute for the same per-model token split.
var stdPrices = PriceTable{
	Models: map[string]ModelPrices{
		"claude-opus-4-7":   {InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.50},
		"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15, CacheCreationPerMTok: 3.75, CacheReadPerMTok: 0.30},
	},
	Default: ModelPrices{InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.50},
}

const epsilon = 0.005 // half a cent — well within a rounded ccusage dollar figure

// TestActiveBlockCostParity is the pinned-baseline parity guard (ADR 0021 Test
// Strategy "native CostPricer vs a pinned ccusage baseline; per-model + cache
// split"). A controlled two-model transcript, all within one 5h block, is
// priced by the native pricer and must equal the hand-computed figure that
// ccusage/LiteLLM would produce for the same per-model token split.
//
// Hand computation (per-MTok prices above):
//
//	opus:   in 1_000 @5, out 2_000 @25, cc 1_000_000 @6.25, cr 5_000_000 @0.50
//	        = 0.005 + 0.05 + 6.25 + 2.50                         = 8.805
//	sonnet: in 500 @3, out 1_000 @15, cc 200_000 @3.75, cr 800_000 @0.30
//	        = 0.0015 + 0.015 + 0.75 + 0.24                       = 1.0065
//	total                                                         = 9.8115
func TestActiveBlockCostParity(t *testing.T) {
	base := time.Date(2026, 4, 23, 19, 30, 0, 0, time.UTC)
	recs := []Record{
		{Timestamp: base, Model: "claude-opus-4-7", Tokens: ModelTokens{Input: 1000, Output: 2000, CacheCreation: 1_000_000, CacheRead: 5_000_000}},
		{Timestamp: base.Add(30 * time.Minute), Model: "claude-sonnet-4-6", Tokens: ModelTokens{Input: 500, Output: 1000, CacheCreation: 200_000, CacheRead: 800_000}},
	}
	now := base.Add(time.Hour) // inside the block window
	block := ActiveBlock(recs, stdPrices, now)
	if block == nil {
		t.Fatal("ActiveBlock = nil, want an active block")
	}
	const pinned = 9.8115
	if diff := block.CostUSD - pinned; diff > epsilon || diff < -epsilon {
		t.Errorf("CostUSD = %.6f, want %.4f (±%v)", block.CostUSD, pinned, epsilon)
	}
	if !block.IsActive {
		t.Error("block.IsActive = false, want true")
	}
}

// TestActiveBlockWindowsByFiveHours proves ccusage-style 5h windowing: records
// more than 5h after the block anchor start a NEW block, and only the block
// whose window contains now is returned active.
func TestActiveBlockWindowsByFiveHours(t *testing.T) {
	anchor := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	recs := []Record{
		// Old block, long finished.
		{Timestamp: anchor, Model: "claude-opus-4-7", Tokens: ModelTokens{Output: 1_000_000}}, // $25
		// New block six hours later — the active one.
		{Timestamp: anchor.Add(6 * time.Hour), Model: "claude-opus-4-7", Tokens: ModelTokens{Output: 2_000_000}}, // $50
	}
	now := anchor.Add(7 * time.Hour)
	block := ActiveBlock(recs, stdPrices, now)
	if block == nil {
		t.Fatal("ActiveBlock = nil, want the recent block")
	}
	// Only the second record ($50) is in the active window; the first ($25) is a stale block.
	if diff := block.CostUSD - 50.0; diff > epsilon || diff < -epsilon {
		t.Errorf("CostUSD = %.4f, want 50.00 (only the active block's record)", block.CostUSD)
	}
}

// TestActiveBlockNoneWhenStale proves that when now is beyond the last block's
// 5h window there is no active block (nil), mirroring ccusage isActive=false.
func TestActiveBlockNoneWhenStale(t *testing.T) {
	anchor := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	recs := []Record{{Timestamp: anchor, Model: "claude-opus-4-7", Tokens: ModelTokens{Output: 1000}}}
	now := anchor.Add(9 * time.Hour) // well past start+5h
	if block := ActiveBlock(recs, stdPrices, now); block != nil {
		t.Errorf("ActiveBlock = %+v, want nil (stale, no active block)", block)
	}
}

// TestActiveBlockEmpty proves no records yields no block.
func TestActiveBlockEmpty(t *testing.T) {
	if block := ActiveBlock(nil, stdPrices, time.Now()); block != nil {
		t.Errorf("ActiveBlock(nil) = %+v, want nil", block)
	}
}

// TestActiveBlockUnknownModelPriced proves an unknown model is still priced via
// the table Default (never dropped to zero — ADR unknown-model policy).
func TestActiveBlockUnknownModelPriced(t *testing.T) {
	base := time.Date(2026, 4, 23, 19, 0, 0, 0, time.UTC)
	recs := []Record{{Timestamp: base, Model: "brand-new-opus", Tokens: ModelTokens{Output: 1_000_000}}}
	block := ActiveBlock(recs, stdPrices, base.Add(time.Hour))
	if block == nil {
		t.Fatal("ActiveBlock = nil")
	}
	// Default output price is 25.
	if diff := block.CostUSD - 25.0; diff > epsilon || diff < -epsilon {
		t.Errorf("CostUSD = %.4f, want 25.00 (Default price for unknown model)", block.CostUSD)
	}
}
