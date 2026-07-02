package usage

import "testing"

// TestModelUsageCost proves per-model cost = sum of each token category times
// its per-million-token price (ADR 0021 §3 "sum per-model input/output/cache
// tokens × prices"). Prices are per-million tokens.
func TestModelUsageCost(t *testing.T) {
	prices := ModelPrices{
		InputPerMTok:         5.0,
		OutputPerMTok:        25.0,
		CacheCreationPerMTok: 6.25,
		CacheReadPerMTok:     0.50,
	}
	tok := ModelTokens{
		Input:         1_000_000,
		Output:        1_000_000,
		CacheCreation: 1_000_000,
		CacheRead:     1_000_000,
	}
	// 5 + 25 + 6.25 + 0.50 = 36.75
	got := prices.Cost(tok)
	if want := 36.75; got != want {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

// TestModelUsageCostZeroTokens proves a zero-token model contributes zero cost
// (ADR Test Strategy: "zero-token" case).
func TestModelUsageCostZeroTokens(t *testing.T) {
	prices := ModelPrices{InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.5}
	if got := prices.Cost(ModelTokens{}); got != 0 {
		t.Errorf("Cost(zero) = %v, want 0", got)
	}
}

// TestPriceTableCostKnownModel proves the table dispatches on model id.
func TestPriceTableCostKnownModel(t *testing.T) {
	pt := PriceTable{
		Models: map[string]ModelPrices{
			"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15, CacheCreationPerMTok: 3.75, CacheReadPerMTok: 0.30},
		},
	}
	// 300 input, 92_638 output, 1_079_545 cc, 15_527_024 cr (per active_block fixture split)
	tok := ModelTokens{Input: 1000, Output: 1000, CacheCreation: 1000, CacheRead: 1000}
	// (3 + 15 + 3.75 + 0.30) * 1000/1e6 = 22.05 * 0.001 = 0.02205
	got, known := pt.Cost("claude-sonnet-4-5", tok)
	if !known {
		t.Fatal("known model reported unknown")
	}
	if want := 0.02205; got != want {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

// TestPriceTableUnknownModelUsesFallback proves an unknown model prices via the
// configured Default entry rather than silently costing zero (ADR Test Strategy:
// "unknown-model policy"). Zero cost for real tokens would understate spend.
func TestPriceTableUnknownModelUsesFallback(t *testing.T) {
	pt := PriceTable{
		Models:  map[string]ModelPrices{},
		Default: ModelPrices{InputPerMTok: 3, OutputPerMTok: 15, CacheCreationPerMTok: 3.75, CacheReadPerMTok: 0.30},
	}
	tok := ModelTokens{Output: 1_000_000}
	got, known := pt.Cost("some-unheard-of-model", tok)
	if known {
		t.Error("unknown model reported known")
	}
	if want := 15.0; got != want {
		t.Errorf("Cost = %v, want %v (fallback Default output price)", got, want)
	}
}
