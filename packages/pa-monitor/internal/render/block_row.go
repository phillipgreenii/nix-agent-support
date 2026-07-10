package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
)

// BlockRowOpts carries time + width context for BlockRow.
type BlockRowOpts struct {
	Now   time.Time
	Width int
	// StaleAfter is how old the authoritative status-line rate_limits capture may
	// be before the 5h percentage renders as stale(age) (ADR 0021 §1). Zero
	// disables staleness (always fresh). Only consulted when the tree carries an
	// authoritative FiveHourPct.
	StaleAfter time.Duration
}

// blockRowBarWidth is the bar width at WIDE/NARROW tiers. TINY drops the bar.
const blockRowBarWidth = 18

// BlockRow returns a single-row, tier-aware 5h block summary.
//
// Pre-active states (any tier):
//
//	"5h loading…"
//	"5h unavailable — cost scan failed"
//	"5h no active block"
//	"5h $X.XX  resets HH:MM  (plan cap unknown)"   when PlanCapUSD <= 0
//
// Active-block tier shapes:
//
//	WIDE   "5h ████████░░░░░░░░░░ 35%  $30.10  1.2M/m  resets 01:00  ex 22:21 ⚠"
//	NARROW "5h ████████░░░░░░░░░░ 35%  $30.10  resets 01:00  ex 22:21"
//	TINY   "5h 35%  resets 01:00"
//
// Drop priority (most-droppable first): burn → bar → cost → exhaust → reset.
// Percent and reset always survive (when an active block exists).
func BlockRow(tree *aggregate.Tree, opts BlockRowOpts) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case !tree.CostProbed:
		return "5h loading…"
	case tree.CostProbeErr != nil:
		return "5h unavailable — cost scan failed"
	case tree.ActiveBlock == nil:
		return "5h no active block"
	}
	block := tree.ActiveBlock

	if tree.PlanCapUSD <= 0 {
		return fmt.Sprintf("5h $%.2f  resets %s  (plan cap unknown)",
			block.CostUSD, block.EndTime.Local().Format("15:04"))
	}

	pct := 100 * block.CostUSD / tree.PlanCapUSD
	tier := wrap.Tier(opts.Width)

	// Authoritative status-line five_hour used_percentage (ADR 0021 §5) wins over
	// the cost-derived percentage when present. It may render "NN%" (fresh) or
	// "stale (age)" (older than StaleAfter); the progress bar still reflects the
	// authoritative percentage when it is a live number. When absent, fall back to
	// the cost-derived percentage (pre-Phase-3 behavior).
	authLabel := RateLimitUsageLabel(tree.FiveHourPct, tree.LimitsCapturedAt, now, opts.StaleAfter)
	barPct := pct
	if authLabel != "" && tree.FiveHourPct != nil {
		barPct = *tree.FiveHourPct
	}

	var sb strings.Builder
	sb.WriteString("5h ")

	if tier != wrap.TierTiny {
		sb.WriteString(progressBar(barPct, blockRowBarWidth))
		sb.WriteString(" ")
	}
	if authLabel != "" {
		sb.WriteString(authLabel)
	} else {
		fmt.Fprintf(&sb, "%.0f%%", pct)
	}

	if tier != wrap.TierTiny {
		fmt.Fprintf(&sb, "  $%.2f", block.CostUSD)
	}

	if tier == wrap.TierWide {
		fmt.Fprintf(&sb, "  %sM/m", fmtM(block.BurnRate.TokensPerMinute))
	}

	fmt.Fprintf(&sb, "  resets %s", block.EndTime.Local().Format("15:04"))

	if tier != wrap.TierTiny {
		exhaust := tree.ProjectedExhaust(now)
		if !exhaust.IsZero() {
			warn := ""
			if exhaust.Before(block.EndTime) {
				warn = " ⚠"
			}
			fmt.Fprintf(&sb, "  ex %s%s", exhaust.Local().Format("15:04"), warn)
		}
	}

	return sb.String()
}

// fmtM formats tokens-per-minute as "1.2" (millions). Used in BlockRow's burn segment.
func fmtM(tokensPerMinute float64) string {
	return fmt.Sprintf("%.1f", tokensPerMinute/1_000_000)
}
