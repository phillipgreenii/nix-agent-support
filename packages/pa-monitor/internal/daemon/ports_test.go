package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

func configWithTier(tier string) config.Config { return config.Config{PlanTier: tier} }

// fakeLimitsSource is a port fake for the LimitsSource interface (ADR 0021 §1
// reader contract). Phase 2 ships no real adapter — this fake exists so the
// interface is exercised and consumers can be tested against it in Phase 3.
type fakeLimitsSource struct {
	limits *Limits
	err    error
}

func (f *fakeLimitsSource) Current(context.Context) (*Limits, error) {
	return f.limits, f.err
}

// TestLimitsSourceFakeSatisfiesPort proves the LimitsSource interface is
// defined and its Limits struct carries the ADR §1 fields (each independently
// optional: nil/zero = unknown, never 0/1970).
func TestLimitsSourceFakeSatisfiesPort(t *testing.T) {
	pct := 34.0
	captured := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	var src LimitsSource = &fakeLimitsSource{
		limits: &Limits{
			FiveHourPct: &pct,
			CapturedAt:  captured,
		},
	}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current err: %v", err)
	}
	if got.FiveHourPct == nil || *got.FiveHourPct != 34.0 {
		t.Errorf("FiveHourPct = %v, want 34", got.FiveHourPct)
	}
	if got.SevenDayPct != nil {
		t.Errorf("SevenDayPct = %v, want nil (unknown)", got.SevenDayPct)
	}
	if !got.CapturedAt.Equal(captured) {
		t.Errorf("CapturedAt = %v, want %v", got.CapturedAt, captured)
	}
}

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
