package usage

// ModelTokens holds the four cumulative token categories a single model
// consumed within a window, mirroring claude-transcript's Usage fields. These
// are the inputs the native CostPricer (ADR 0021 §3) multiplies by per-model
// prices.
type ModelTokens struct {
	Input         int
	Output        int
	CacheCreation int
	CacheRead     int
	// CacheCreationEphemeral1h and CacheCreationEphemeral5m are the per-TTL
	// breakdown of CacheCreation (a 1h cache write costs 2.0x a model's base
	// input price against 5m's 1.25x, pg2-xgzen). Both are a subset already
	// counted in CacheCreation; ModelPrices.Cost below does not yet price them
	// separately, so they are carried through purely as measured data for a
	// future pricing change to consume.
	CacheCreationEphemeral1h int
	CacheCreationEphemeral5m int
}

// ModelPrices is the per-million-token USD price for each token category.
// Values mirror Anthropic's published per-token pricing (e.g. Opus 4.x:
// input 5, output 25, cache-write-5m 6.25, cache-read 0.50). ccusage/LiteLLM
// price the same categories the same way; this is what makes native cost match
// the pinned ccusage baseline (see costpricer_native_test.go).
type ModelPrices struct {
	InputPerMTok         float64
	OutputPerMTok        float64
	CacheCreationPerMTok float64
	CacheReadPerMTok     float64
}

// Cost returns the USD cost of the given tokens at these prices. Prices are per
// one million tokens, so each category is (tokens/1e6 × pricePerMTok).
func (p ModelPrices) Cost(t ModelTokens) float64 {
	const m = 1_000_000.0
	return float64(t.Input)/m*p.InputPerMTok +
		float64(t.Output)/m*p.OutputPerMTok +
		float64(t.CacheCreation)/m*p.CacheCreationPerMTok +
		float64(t.CacheRead)/m*p.CacheReadPerMTok
}

// PriceTable maps model ids to their prices, with a Default used for any model
// not present in the map. It is built from the Account's [account.pricing]
// config (ADR 0021 §3 "reading prices from the Account").
type PriceTable struct {
	Models  map[string]ModelPrices
	Default ModelPrices
}

// Cost returns the USD cost for one model's tokens and whether the model was
// found in the table. Unknown models price via Default (never silently zero —
// zeroing real tokens would understate spend; ADR Test Strategy unknown-model
// policy).
func (pt PriceTable) Cost(model string, t ModelTokens) (float64, bool) {
	if pr, ok := pt.Models[model]; ok {
		return pr.Cost(t), true
	}
	return pt.Default.Cost(t), false
}
