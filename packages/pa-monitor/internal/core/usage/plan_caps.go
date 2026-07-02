package usage

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

// WeekCapUSD returns the per-week soft cap for a plan tier in USD.
//
// Anthropic does NOT publish a dollar figure for the weekly limit — they
// publish hours of Claude Code usage instead. As of 2026-05, the published
// weekly ranges are:
//
//	Pro       : 40–80 hours of Sonnet 4 only
//	Max 5x    : 140–280 hours of Sonnet 4  +  15–35 hours of Opus 4
//	Max 20x   : 240–480 hours of Sonnet 4  +  24–40 hours of Opus 4
//
// pa-monitor measures weekly usage in USD via ccusage (token volume × per-token
// pricing). The conversion is non-deterministic: real $/hour varies with how
// hard the user pushes the model. The caps below use a conservative
// upper-bound estimate so the soft-cap warning fires somewhat before the
// actual hour-based limit kicks in. Tune per personal observation.
//
// Reference: Anthropic announced weekly limits in 2025-08; figures above
// reflect the 2026-04 increase ("50% boost through July 13, 2026").
func WeekCapUSD(tier string) float64 {
	switch tier {
	case "pro":
		// ~80h × ~$4/h (Sonnet) ≈ $320; cap at $300 to warn early.
		return 300
	case "max_5x":
		// ~280h Sonnet × ~$5/h + ~35h Opus × ~$15/h ≈ $1925; round to $2000.
		return 2000
	case "max_20x":
		// ~480h Sonnet × ~$6/h + ~40h Opus × ~$20/h ≈ $3680; round to $4000.
		return 4000
	default:
		return 0
	}
}
