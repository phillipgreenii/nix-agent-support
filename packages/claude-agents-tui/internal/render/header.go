package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
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
}

func Header(tree *aggregate.Tree, opts HeaderOpts) string {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var sb strings.Builder

	caff := "○ off"
	if opts.CaffeinateOn {
		if opts.GraceRemaining > 0 {
			caff = fmt.Sprintf("● on %ds", int(opts.GraceRemaining.Seconds()))
		} else {
			caff = "● on"
		}
	}

	th := opts.Theme
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

	autoResumeLabel := "○ off"
	if opts.AutoResume {
		autoResumeLabel = th.ActiveToggle.Render("● on")
	}
	fmt.Fprintf(&sb, "[C]affeinate: %s  |  [t] %s · %s  |  [a] %s · %s  |  [n] %s · %s  |  [q]\n",
		caff, tokLabel, costLabel, activeLabel, allLabel, nameLabel, idLabel)
	fmt.Fprintf(&sb, "[R] auto-resume: %s  |  [M] resume now\n\n", autoResumeLabel)

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
