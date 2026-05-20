package ccusage

// PlanCapUSD returns the per-5h-block soft cap for a plan tier. Figures are
// approximate ccusage-published budget mappings. Update when Anthropic changes.
// Unknown tiers return 0 (meaning: do not compute exhaust time).
func PlanCapUSD(tier string) float64 {
	switch tier {
	case "pro":
		return 18
	case "max_5x":
		return 90
	case "max_20x":
		return 360
	default:
		return 0
	}
}

// WeekCapUSD returns the per-week soft cap for a plan tier. Anthropic's
// weekly limit was introduced in 2025-08; published figures are not
// authoritative in this codebase yet. Values below are placeholders to
// unblock implementation — confirm and update from Anthropic's published
// limits before relying on emitted limit-hit events.
//
// TODO: confirm WeekCapUSD values from Anthropic's published plan limits.
func WeekCapUSD(tier string) float64 {
	switch tier {
	case "pro":
		return 50
	case "max_5x":
		return 200
	case "max_20x":
		return 800
	default:
		return 0
	}
}
