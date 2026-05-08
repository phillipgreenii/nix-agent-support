package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// BlockRowOpts carries time + width context for BlockRow.
type BlockRowOpts struct {
	Now   time.Time
	Width int
}

// blockRowBarWidth is the bar width at WIDE/NARROW tiers. TINY drops the bar.
const blockRowBarWidth = 18

// BlockRow returns a single-row, tier-aware 5h block summary.
//
// Pre-active states (any tier):
//
//	"5h loading…"
//	"5h unavailable — `ccusage` not on PATH"
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
	case !tree.CCUsageProbed:
		return "5h loading…"
	case tree.CCUsageErr != nil:
		return "5h unavailable — `ccusage` not on PATH"
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

	var sb strings.Builder
	sb.WriteString("5h ")

	if tier != wrap.TierTiny {
		sb.WriteString(progressBar(pct, blockRowBarWidth))
		sb.WriteString(" ")
	}
	fmt.Fprintf(&sb, "%.0f%%", pct)

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
