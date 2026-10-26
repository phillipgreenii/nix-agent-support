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

// pg2-u2sv: transcript model ids carry a date suffix (e.g.
// claude-haiku-4-5-20251001). Exact-match keys miss them and fall through to
// _default (opus 15/75), over-charging cheap models ~15×. Prefix-match the
// longest table key so a date-suffixed haiku id prices as haiku.
func TestEstimateCents_prefixMatchesDateSuffix(t *testing.T) {
	pt := DefaultPrices()
	s := Snapshot{Model: "claude-haiku-4-5-20251001", InputTokens: 1_000_000, OutputTokens: 1_000_000}
	// haiku: $1 + $5 = $6 -> 600 cents (NOT the _default opus $90 -> 9000).
	if got := EstimateCents(s, pt); got != 600 {
		t.Errorf("EstimateCents = %d, want 600 (haiku via prefix match, not _default)", got)
	}
}

// An exact key match must still work (key == model is a prefix of itself), and
// the longest matching key wins.
func TestEstimateCents_exactAndLongestPrefix(t *testing.T) {
	pt := DefaultPrices()
	s := Snapshot{Model: "claude-sonnet-4-6", OutputTokens: 1_000_000} // exact, no suffix
	if got := EstimateCents(s, pt); got != 1500 {                      // sonnet $15/MTok out
		t.Errorf("EstimateCents = %d, want 1500 (sonnet exact)", got)
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
