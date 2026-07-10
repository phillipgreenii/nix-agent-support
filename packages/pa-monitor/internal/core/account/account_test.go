package account

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// TestLoadAccountCapsMatchPublishedBudgets is the pinned no-behavior-change guard: the
// caps the Account carries MUST be byte-identical to the legacy
// usage.PlanCapUSD / usage.WeekCapUSD figures for every tier (including the
// unknown-tier → 0 case). Any drift here silently shifts plan_cap_usd /
// week_cap_usd metrics, proto WindowPct, and the exhaust projection.
func TestLoadAccountCapsMatchPublishedBudgets(t *testing.T) {
	for _, tier := range []string{"pro", "max_5x", "max_20x", "unknown", ""} {
		acct := LoadAccount(config.Config{PlanTier: tier})
		if got, want := acct.BlockCap(), usage.PlanCapUSD(tier); got != want {
			t.Errorf("BlockCap(%q) = %v, want %v (must match usage.PlanCapUSD)", tier, got, want)
		}
		if got, want := acct.WeekCap(), usage.WeekCapUSD(tier); got != want {
			t.Errorf("WeekCap(%q) = %v, want %v (must match usage.WeekCapUSD)", tier, got, want)
		}
		if acct.PlanTier != tier {
			t.Errorf("PlanTier = %q, want %q", acct.PlanTier, tier)
		}
	}
}

// TestLoadAccountPriceTable proves the Account exposes the config's per-model
// price table for the native CostPricer (ADR 0021 §3 "reading prices from the
// Account"). Prices carry through verbatim, and the unknown-model fallback is
// the config Default.
func TestLoadAccountPriceTable(t *testing.T) {
	cfg := config.Config{
		PlanTier: "max_5x",
		Pricing: config.PricingConfig{
			Default: config.ModelPricing{InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.5},
			Models: map[string]config.ModelPricing{
				"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15, CacheCreationPerMTok: 3.75, CacheReadPerMTok: 0.3},
			},
		},
	}
	pt := LoadAccount(cfg).PriceTable()
	// Known model: 1M output sonnet @ $15.
	if got, known := pt.Cost("claude-sonnet-4-6", usage.ModelTokens{Output: 1_000_000}); !known || got != 15.0 {
		t.Errorf("sonnet Cost = (%v,%v), want (15,true)", got, known)
	}
	// Unknown model falls back to Default output $25.
	if got, known := pt.Cost("mystery", usage.ModelTokens{Output: 1_000_000}); known || got != 25.0 {
		t.Errorf("unknown Cost = (%v,%v), want (25,false)", got, known)
	}
}
