package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

type HeaderOpts struct {
	CaffeinateOn    bool
	GraceRemaining  time.Duration
	TopupPoolUSD    float64
	TopupConsumed   float64
	Now             time.Time
	ShowAll         bool
	CostMode        bool
	ForceID         bool
	Theme           Theme
	AutoResume      bool
	WindowResetsAt  time.Time
	AutoResumeDelay time.Duration
	Width           int // terminal columns; 0 falls back to wrap.FallbackWidth
}

func Header(tree *aggregate.Tree, opts HeaderOpts) string {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var sb strings.Builder

	caffWide := "○ off"
	caffGlyph := "○"
	if opts.CaffeinateOn {
		caffGlyph = "●"
		if opts.GraceRemaining > 0 {
			caffWide = fmt.Sprintf("● on %ds", int(opts.GraceRemaining.Seconds()))
		} else {
			caffWide = "● on"
		}
	}

	th := opts.Theme
	autoResumeWide := "○ off"
	autoResumeGlyph := "○"
	if opts.AutoResume {
		autoResumeWide = th.ActiveToggle.Render("● on")
		autoResumeGlyph = th.ActiveToggle.Render("●")
	}

	tier := wrap.Tier(opts.Width)
	switch tier {
	case wrap.TierWide:
		tokLabel, costLabel := "tokens", "cost"
		if opts.CostMode {
			costLabel = th.ActiveToggle.Render("cost")
		} else {
			tokLabel = th.ActiveToggle.Render("tokens")
		}
		activeLabel, allLabel := "active", "all"
		if opts.ShowAll {
			allLabel = th.ActiveToggle.Render("all")
		} else {
			activeLabel = th.ActiveToggle.Render("active")
		}
		nameLabel, idLabel := "name", "id"
		if opts.ForceID {
			idLabel = th.ActiveToggle.Render("id")
		} else {
			nameLabel = th.ActiveToggle.Render("name")
		}
		fmt.Fprintf(&sb, "[C]affeinate: %s  |  [t] %s · %s  |  [a] %s · %s  |  [n] %s · %s  |  [q]\n",
			caffWide, tokLabel, costLabel, activeLabel, allLabel, nameLabel, idLabel)
		fmt.Fprintf(&sb, "[R] auto-resume: %s  |  [M] resume now\n\n", autoResumeWide)
	case wrap.TierNarrow:
		tokLabel, costLabel := "tok", "cost"
		if opts.CostMode {
			costLabel = th.ActiveToggle.Render("cost")
		} else {
			tokLabel = th.ActiveToggle.Render("tok")
		}
		actLabel, allLabel := "act", "all"
		if opts.ShowAll {
			allLabel = th.ActiveToggle.Render("all")
		} else {
			actLabel = th.ActiveToggle.Render("act")
		}
		nmLabel, idLabel := "nm", "id"
		if opts.ForceID {
			idLabel = th.ActiveToggle.Render("id")
		} else {
			nmLabel = th.ActiveToggle.Render("nm")
		}
		fmt.Fprintf(&sb, "[C] %s  |  [t] %s · %s  |  [a] %s · %s  |  [n] %s · %s  |  [q]\n",
			caffWide, tokLabel, costLabel, actLabel, allLabel, nmLabel, idLabel)
		fmt.Fprintf(&sb, "[R] auto: %s  |  [M] resume\n\n", autoResumeWide)
	default: // TierTiny — keys-only, show only active variant per toggle
		tokOrCost := "tok"
		if opts.CostMode {
			tokOrCost = "cost"
		}
		actOrAll := "act"
		if opts.ShowAll {
			actOrAll = "all"
		}
		nmOrID := "nm"
		if opts.ForceID {
			nmOrID = "id"
		}
		fmt.Fprintf(&sb, "[C]%s  [t]%s  [a]%s  [n]%s  [q]\n",
			caffGlyph, th.ActiveToggle.Render(tokOrCost), th.ActiveToggle.Render(actOrAll), th.ActiveToggle.Render(nmOrID))
		fmt.Fprintf(&sb, "[R]%s  [M]now\n\n", autoResumeGlyph)
	}

	switch {
	case !tree.CCUsageProbed:
		sb.WriteString("5h Block   loading…\n")
	case tree.CCUsageErr != nil:
		sb.WriteString("5h Block   (unavailable — `ccusage` not found on PATH)\n")
	case tree.ActiveBlock == nil:
		sb.WriteString("5h Block   no active block\n")
	case tree.PlanCapUSD <= 0:
		fmt.Fprintf(&sb, "5h Block   $%.2f   resets %s   (plan cap unknown)\n",
			tree.ActiveBlock.CostUSD,
			tree.ActiveBlock.EndTime.Local().Format("15:04"))
	default:
		pct := 100 * tree.ActiveBlock.CostUSD / tree.PlanCapUSD
		fmt.Fprintf(&sb, "5h Block   %s  %.0f%%   $%.2f   resets %s\n",
			progressBar(pct, 30),
			pct,
			tree.ActiveBlock.CostUSD,
			tree.ActiveBlock.EndTime.Local().Format("15:04"))

		exhaust := tree.ProjectedExhaust(opts.Now)
		if !exhaust.IsZero() {
			rem := exhaust.Sub(opts.Now)
			warn := ""
			if exhaust.Before(tree.ActiveBlock.EndTime) {
				warn = "  ⚠"
			}
			fmt.Fprintf(&sb, "           exhaust at %s (%s)%s\n",
				exhaust.Local().Format("15:04"),
				humanDur(rem),
				warn)
		}
	}

	if tree.TopupShouldDisplay() && opts.TopupPoolUSD > 0 {
		remaining := opts.TopupPoolUSD - opts.TopupConsumed
		pct := 100 * remaining / opts.TopupPoolUSD
		fmt.Fprintf(&sb, "Top-up     %s  %.0f%%   $%.2f / $%.2f remaining\n",
			progressBar(100-pct, 30),
			pct,
			remaining,
			opts.TopupPoolUSD)
	}

	if tree.ActiveBlock != nil {
		fmt.Fprintf(&sb, "Burn       %sk/m\n", fmtK(tree.ActiveBlock.BurnRate.TokensPerMinute))
	}

	fmt.Fprintf(&sb, "Updated %s\n", opts.Now.Format("15:04:05"))
	if opts.AutoResume && !opts.WindowResetsAt.IsZero() {
		fireAt := opts.WindowResetsAt.Add(opts.AutoResumeDelay)
		remaining := fireAt.Sub(opts.Now)
		if remaining > 0 {
			mins := int(remaining.Minutes())
			secs := int(remaining.Seconds()) - mins*60
			fmt.Fprintf(&sb, "⏸ resuming in %d:%02d\n", mins, secs)
		} else {
			sb.WriteString("⏸ resuming…\n")
		}
	}
	return sb.String()
}

func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func humanDur(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func fmtK(v float64) string {
	return fmt.Sprintf("%.0f", v/1000)
}
