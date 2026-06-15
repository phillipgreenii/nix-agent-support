package usage

import "strings"

// ModelPrice is per-MTok USD pricing for one model.
type ModelPrice struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// PriceTable maps a model id to its prices. The special key "_default" is the
// fallback for an unknown model.
type PriceTable map[string]ModelPrice

// DefaultPrices is the built-in table (USD/MTok). Update as Anthropic pricing
// changes; this is data, not logic. Values current as of 2026-06.
func DefaultPrices() PriceTable {
	return PriceTable{
		"_default":          {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5},
		"claude-opus-4-8":   {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5},
		"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15, CacheWritePerMTok: 3.75, CacheReadPerMTok: 0.3},
		"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5, CacheWritePerMTok: 1.25, CacheReadPerMTok: 0.1},
	}
}

// priceFor resolves the price for a model id. Transcript model ids may carry a
// date suffix (e.g. "claude-haiku-4-5-20251001"); an exact-match lookup misses
// those and silently falls through to "_default" (opus 15/75), over-charging
// cheaper models many-fold. So match the LONGEST table key that is a prefix of
// the model id (an exact id is its own prefix), falling back to "_default" only
// when nothing matches. (pg2-u2sv)
func priceFor(model string, t PriceTable) ModelPrice {
	best := ""
	for k := range t {
		if k == "_default" {
			continue
		}
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return t[best]
	}
	return t["_default"]
}

// EstimateCents estimates a Snapshot's cost in integer cents (truncated toward
// zero). The model price is resolved by longest-prefix match (see priceFor); a
// model matching no key falls back to "_default".
func EstimateCents(s Snapshot, t PriceTable) int64 {
	p := priceFor(s.Model, t)
	dollars := float64(s.InputTokens)/1e6*p.InputPerMTok +
		float64(s.OutputTokens)/1e6*p.OutputPerMTok +
		float64(s.CacheCreationTokens)/1e6*p.CacheWritePerMTok +
		float64(s.CacheReadTokens)/1e6*p.CacheReadPerMTok
	return int64(dollars * 100)
}
