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
