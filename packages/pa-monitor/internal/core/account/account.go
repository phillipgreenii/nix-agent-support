// Package account holds the plain Account model (plan identity, pricing
// inputs, budgets) built from config by LoadAccount. Per ADR 0021 §3 the
// Account is a plain struct, NOT a port — a discovery interface is added only
// if/when runtime discovery is actually specified.
//
// The Account carries two kinds of "price" data: the per-plan USD soft caps
// (block/week limits the trackers and store-conversion path consume) and, as of
// Phase 4, the per-model price table the native CostPricer prices transcript
// usage with (ADR 0021 §3 "reading prices from the Account"). Both are sourced
// from config; the caps keep matching the legacy usage.*CapUSD figures (pinned
// guard) so downstream metrics are unchanged.
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

	// prices is the per-model price table the native CostPricer prices with.
	prices usage.PriceTable
}

// LoadAccount builds an Account from config. The caps are sourced from the
// legacy cap tables so the values are byte-identical to the pre-port behavior
// (the pinned test asserts this). The price table is built from
// [account.pricing] (with built-in defaults), decoupling the native pricer from
// any hardcoded price constants.
func LoadAccount(cfg config.Config) Account {
	return Account{
		PlanTier:    cfg.PlanTier,
		blockCapUSD: usage.PlanCapUSD(cfg.PlanTier),
		weekCapUSD:  usage.WeekCapUSD(cfg.PlanTier),
		prices:      priceTableFromConfig(cfg.Pricing),
	}
}

// BlockCap returns the per-5h-block soft cap in USD (0 = unknown).
func (a Account) BlockCap() float64 { return a.blockCapUSD }

// WeekCap returns the per-week soft cap in USD (0 = unknown).
func (a Account) WeekCap() float64 { return a.weekCapUSD }

// PriceTable returns the per-model price table the native CostPricer uses.
func (a Account) PriceTable() usage.PriceTable { return a.prices }

// priceTableFromConfig maps the config's plain pricing shape into the usage
// domain type the pricer consumes (config stays free of a usage dependency).
func priceTableFromConfig(pc config.PricingConfig) usage.PriceTable {
	pt := usage.PriceTable{
		Default: toModelPrices(pc.Default),
		Models:  map[string]usage.ModelPrices{},
	}
	for name, mp := range pc.Models {
		pt.Models[name] = toModelPrices(mp)
	}
	return pt
}

func toModelPrices(mp config.ModelPricing) usage.ModelPrices {
	return usage.ModelPrices{
		InputPerMTok:         mp.InputPerMTok,
		OutputPerMTok:        mp.OutputPerMTok,
		CacheCreationPerMTok: mp.CacheCreationPerMTok,
		CacheReadPerMTok:     mp.CacheReadPerMTok,
	}
}
