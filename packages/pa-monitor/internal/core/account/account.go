// Package account holds the plain Account model (plan identity, pricing
// inputs, budgets) built from config by LoadAccount. Per ADR 0021 §3 the
// Account is a plain struct, NOT a port — a discovery interface is added only
// if/when runtime discovery is actually specified.
//
// In Phase 2 the "prices" the Account carries are the per-plan USD caps that
// the block/week trackers and the store-conversion path consume. Sourcing them
// from the Account (instead of calling usage.PlanCapUSD / usage.WeekCapUSD
// inline) removes the consumers' direct coupling to the ccusage provider while
// keeping the numbers identical (see account_test.go's pinned guard). Native
// per-token pricing is a Phase 4 concern; no price table lives here yet.
package account

import (
	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// Account is the plain plan/pricing/budget model. Built by LoadAccount from the
// nix-rendered config; it is intentionally a value type with no behavior beyond
// exposing its pricing inputs.
type Account struct {
	// PlanTier is the plan identity (e.g. "max_5x"), carried verbatim from
	// config so downstream metric labels are unchanged.
	PlanTier string

	// blockCapUSD / weekCapUSD are the per-5h-block and per-week soft caps in
	// USD. Zero means "unknown" — do not compute exhaust time (matches the
	// legacy usage.*CapUSD unknown-tier semantics exactly).
	blockCapUSD float64
	weekCapUSD  float64
}

// LoadAccount builds an Account from config. The caps are sourced from the
// legacy ccusage cap tables so the values are byte-identical to the pre-port
// behavior (the pinned test asserts this); usage.PlanCapUSD / WeekCapUSD are
// retained as the reference until they are retired in Phase 4.
func LoadAccount(cfg config.Config) Account {
	return Account{
		PlanTier:    cfg.PlanTier,
		blockCapUSD: usage.PlanCapUSD(cfg.PlanTier),
		weekCapUSD:  usage.WeekCapUSD(cfg.PlanTier),
	}
}

// BlockCap returns the per-5h-block soft cap in USD (0 = unknown).
func (a Account) BlockCap() float64 { return a.blockCapUSD }

// WeekCap returns the per-week soft cap in USD (0 = unknown).
func (a Account) WeekCap() float64 { return a.weekCapUSD }
