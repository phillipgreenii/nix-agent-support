package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

func configWithTier(tier string) config.Config { return config.Config{PlanTier: tier} }

// TestBlockToStoreBlockUsesAccountCap proves the store-conversion helper takes
// the cap from the Account (not an inline usage.PlanCapUSD lookup) and that
// the value is identical to the legacy figure for the tier.
func TestBlockToStoreBlockUsesAccountCap(t *testing.T) {
	acct := account.LoadAccount(configWithTier("max_20x"))
	now := time.Now()
	b := &usage.Block{ID: "b1", CostUSD: 100}

	sb := blockToStoreBlock(b, acct.BlockCap(), now)
	if sb.PlanCapUSD != usage.PlanCapUSD("max_20x") {
		t.Errorf("PlanCapUSD = %v, want %v (from Account, matching ccusage)", sb.PlanCapUSD, usage.PlanCapUSD("max_20x"))
	}
	if sb.TotalCostUSD != 100 {
		t.Errorf("TotalCostUSD = %v, want 100", sb.TotalCostUSD)
	}
}

// TestWeekToStoreWeekUsesAccountCap is the weekly counterpart.
func TestWeekToStoreWeekUsesAccountCap(t *testing.T) {
	acct := account.LoadAccount(configWithTier("max_20x"))
	now := time.Now()
	w := &usage.WeeklyEntry{Period: "2026-06-01", TotalCost: 500}

	sw := weekToStoreWeek(w, acct.WeekCap(), now)
	if sw.WeekCapUSD != usage.WeekCapUSD("max_20x") {
		t.Errorf("WeekCapUSD = %v, want %v (from Account, matching ccusage)", sw.WeekCapUSD, usage.WeekCapUSD("max_20x"))
	}
	if sw.TotalCostUSD != 500 {
		t.Errorf("TotalCostUSD = %v, want 500", sw.TotalCostUSD)
	}
}
