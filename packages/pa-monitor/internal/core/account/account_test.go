package account

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// TestLoadAccountCapsMatchCcusage is the pinned no-behavior-change guard: the
// caps the Account carries MUST be byte-identical to the legacy
// usage.PlanCapUSD / usage.WeekCapUSD figures for every tier (including the
// unknown-tier → 0 case). Any drift here silently shifts plan_cap_usd /
// week_cap_usd metrics, proto WindowPct, and the exhaust projection.
func TestLoadAccountCapsMatchCcusage(t *testing.T) {
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
