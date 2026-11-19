package quota

// Prices is the per-million-token USD price of each counter.
type Prices struct{ Input, Output, CacheCreation, CacheRead float64 }

// Cost returns the USD cost of t at these prices.
func (p Prices) Cost(t Tokens) float64 {
	const m = 1_000_000.0
	return float64(t.Input)/m*p.Input + float64(t.Output)/m*p.Output +
		float64(t.CacheCreation)/m*p.CacheCreation + float64(t.CacheRead)/m*p.CacheRead
}

// unknownModel prices a model the table does not list at the priciest common
// tier, so an unknown model never understates spend.
var unknownModel = Prices{Input: 5, Output: 25, CacheCreation: 6.25, CacheRead: 0.50}

// prices is a copy of pa-monitor's built-in price table
// (packages/pa-monitor/internal/config/config.go defaultPricing; cache
// creation is the 5-minute-TTL write). pa-monitor's is internal to its module,
// so the table is copied; keep the two in step.
var prices = map[string]Prices{
	"claude-opus-4-7":           {5, 25, 6.25, 0.50},
	"claude-opus-4-6":           {5, 25, 6.25, 0.50},
	"claude-opus-4-5":           {5, 25, 6.25, 0.50},
	"claude-opus-5-5":           {4, 20, 5.00, 0.20},
	"claude-sonnet-5-5":         {2, 10, 2.50, 0.20},
	"claude-sonnet-4-6":         {3, 15, 3.75, 0.30},
	"claude-sonnet-4-5":         {3, 15, 3.75, 0.30},
	"claude-haiku-4-5-20251001": {1, 5, 1.25, 0.10},
}

// Price returns the prices of a model id (a "[1m]" suffix is ignored).
func Price(model string) Prices {
	if len(model) > 4 && model[len(model)-4:] == "[1m]" {
		model = model[:len(model)-4]
	}
	if p, ok := prices[model]; ok {
		return p
	}
	return unknownModel
}
