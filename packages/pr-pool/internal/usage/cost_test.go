package usage

import "testing"

func TestEstimateCents_knownModel(t *testing.T) {
	pt := PriceTable{"m": {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5}}
	s := Snapshot{Model: "m", InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000}
	// $15 + $75 + $1.5 + $18.75 = $110.25 -> 11025 cents
	if got := EstimateCents(s, pt); got != 11025 {
		t.Errorf("EstimateCents = %d, want 11025", got)
	}
}

func TestEstimateCents_unknownModelFallback(t *testing.T) {
	pt := PriceTable{"_default": {InputPerMTok: 3, OutputPerMTok: 15}}
	s := Snapshot{Model: "unknown", InputTokens: 1_000_000, OutputTokens: 1_000_000}
	// fallback: $3 + $15 = $18 -> 1800 cents
	if got := EstimateCents(s, pt); got != 1800 {
		t.Errorf("EstimateCents = %d, want 1800 (default fallback)", got)
	}
}

func TestEstimateCents_largeMagnitudeNoOverflow(t *testing.T) {
	pt := PriceTable{"m": {CacheReadPerMTok: 1.5}}
	s := Snapshot{Model: "m", CacheReadTokens: 50_000_000} // tens of millions
	// 50M * $1.5/MTok = $75 -> 7500 cents
	if got := EstimateCents(s, pt); got != 7500 {
		t.Errorf("EstimateCents = %d, want 7500", got)
	}
}
