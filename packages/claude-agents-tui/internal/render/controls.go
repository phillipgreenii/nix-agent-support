package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// ControlsOpts carries everything Controls needs to render the toggle row.
type ControlsOpts struct {
	CaffeinateOn   bool
	GraceRemaining time.Duration
	ShowAll        bool
	CostMode       bool
	ForceID        bool
	AutoResume     bool
	Theme          Theme
	Width          int
}

// Controls returns a single-row, tier-aware controls line:
//
//	WIDE   ≥120  [C] ● on  [t] tokens · cost  [a] active · all  [n] name · id  [R] ● on  [M] now  [?]  [q]
//	NARROW 80–119 [C]●  [t] tok · cost  [a] act · all  [n] nm · id  [R]●  [M]now  [?][q]
//	TINY   <80   [C]●  [t]tok  [a]act  [n]nm  [R]●  [M]now  [?][q]
//
// The active half of each toggle is highlighted via theme.ActiveToggle.
// At TINY only the active half of each toggle is shown.
func Controls(opts ControlsOpts) string {
	th := opts.Theme

	caffWide := "○ off"
	caffGlyph := "○"
	if opts.CaffeinateOn {
		caffGlyph = th.ActiveToggle.Render("●")
		if opts.GraceRemaining > 0 {
			caffWide = th.ActiveToggle.Render(fmt.Sprintf("● on %ds", int(opts.GraceRemaining.Seconds())))
		} else {
			caffWide = th.ActiveToggle.Render("● on")
		}
	}

	autoResumeWide := "○ off"
	autoResumeGlyph := "○"
	if opts.AutoResume {
		autoResumeWide = th.ActiveToggle.Render("● on")
		autoResumeGlyph = th.ActiveToggle.Render("●")
	}

	var sb strings.Builder
	switch wrap.Tier(opts.Width) {
	case wrap.TierWide:
		tokLabel, costLabel := "tokens", "cost"
		if opts.CostMode {
			costLabel = th.ActiveToggle.Render("cost")
		} else {
			tokLabel = th.ActiveToggle.Render("tokens")
		}
		actLabel, allLabel := "active", "all"
		if opts.ShowAll {
			allLabel = th.ActiveToggle.Render("all")
		} else {
			actLabel = th.ActiveToggle.Render("active")
		}
		nameLabel, idLabel := "name", "id"
		if opts.ForceID {
			idLabel = th.ActiveToggle.Render("id")
		} else {
			nameLabel = th.ActiveToggle.Render("name")
		}
		fmt.Fprintf(&sb, "[C] %s  [t] %s · %s  [a] %s · %s  [n] %s · %s  [R] %s  [M] now  [?]  [q]",
			caffWide, tokLabel, costLabel, actLabel, allLabel, nameLabel, idLabel, autoResumeWide)
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
		fmt.Fprintf(&sb, "[C]%s  [t] %s · %s  [a] %s · %s  [n] %s · %s  [R]%s  [M]now  [?][q]",
			caffGlyph, tokLabel, costLabel, actLabel, allLabel, nmLabel, idLabel, autoResumeGlyph)
	default: // TierTiny
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
		fmt.Fprintf(&sb, "[C]%s  [t]%s  [a]%s  [n]%s  [R]%s  [M]now  [?][q]",
			caffGlyph, th.ActiveToggle.Render(tokOrCost), th.ActiveToggle.Render(actOrAll), th.ActiveToggle.Render(nmOrID), autoResumeGlyph)
	}
	return sb.String()
}
