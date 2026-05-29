package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
)

// ControlsOpts carries everything Controls needs to render the toggle row.
type ControlsOpts struct {
	CaffeinateOn   bool
	GraceRemaining time.Duration
	ShowAll        bool
	CostMode       bool
	ForceID        bool
	AutoResume     bool
	// DaemonConnected drives the upper-left daemon RPC connection indicator.
	// true  -> filled green dot ("●") meaning the latest poll succeeded
	// false -> hollow dim dot  ("○") meaning the latest poll failed or
	//          no poll has happened yet
	// Prepended to the controls row at all tiers so the user can see at a
	// glance whether the daemon RPC is alive.
	DaemonConnected bool
	Theme           Theme
	Width           int
}

// Controls returns a single-row, tier-aware controls line:
//
//	WIDE   ≥120  ● Caffeinated Enabled  [t] tokens · cost  [a] active · all  [n] name · id  Auto Nudge Enabled  [N] nudge  [?]  [q]
//	NARROW 80–119 ● [C]●  [t] tok · cost  [a] act · all  [n] nm · id  [R]●  [N]nudge  [?][q]
//	TINY   <80   ● [C]●  [t]tok  [a]act  [n]nm  [R]●  [N]nudge  [?][q]
//
// The leading glyph is the daemon RPC connection indicator: filled "●" when
// the latest poll succeeded, hollow "○" when offline. The active half of
// each toggle is highlighted via theme.ActiveToggle. At WIDE the caffeinate
// and auto-nudge toggles use prose ("Enabled" / "Disabled"); at NARROW/TINY
// they collapse to glyphs to fit the width budget. At TINY only the active
// half of each binary toggle is shown.
func Controls(opts ControlsOpts) string {
	th := opts.Theme

	// Daemon connection indicator. Use theme.Working for "alive" so it picks
	// up the green palette where colours are available; Dormant (faint) for
	// "offline". In mono terminals these fall back to bold/faint via theme.
	var daemonGlyph string
	if opts.DaemonConnected {
		daemonGlyph = th.Working.Render("●")
	} else {
		daemonGlyph = th.Dormant.Render("○")
	}

	// Wide-form labels: prose "Enabled" / "Disabled". Highlight (bold/colour)
	// the phrase when the feature is on; render plain when off. Grace
	// countdown appended in parens when caffeinate is in its post-work
	// cooldown. The [C] / [R] key hints are dropped at WIDE -- the prose
	// labels are clear enough on their own, and the help modal ([?]) lists
	// every keybinding.
	caffWideOn := "Caffeinated Enabled"
	if opts.GraceRemaining > 0 {
		caffWideOn = fmt.Sprintf("Caffeinated Enabled (%ds)", int(opts.GraceRemaining.Seconds()))
	}
	caffWide := "Caffeinated Disabled"
	caffGlyph := "○"
	if opts.CaffeinateOn {
		caffGlyph = th.ActiveToggle.Render("●")
		caffWide = th.ActiveToggle.Render(caffWideOn)
	}

	autoResumeWide := "Auto Nudge Disabled"
	autoResumeGlyph := "○"
	if opts.AutoResume {
		autoResumeWide = th.ActiveToggle.Render("Auto Nudge Enabled")
		autoResumeGlyph = th.ActiveToggle.Render("●")
	}

	var sb strings.Builder
	// Prepend the daemon indicator at all tiers; it costs 2 cells (glyph +
	// space) and must always be visible even at TierTiny.
	sb.WriteString(daemonGlyph)
	sb.WriteString(" ")
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
		fmt.Fprintf(&sb, "%s  [t] %s · %s  [a] %s · %s  [n] %s · %s  %s  [N] nudge  [?]  [q]",
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
		fmt.Fprintf(&sb, "[C]%s  [t] %s · %s  [a] %s · %s  [n] %s · %s  [R]%s  [N]nudge  [?][q]",
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
		fmt.Fprintf(&sb, "[C]%s  [t]%s  [a]%s  [n]%s  [R]%s  [N]nudge  [?][q]",
			caffGlyph, th.ActiveToggle.Render(tokOrCost), th.ActiveToggle.Render(actOrAll), th.ActiveToggle.Render(nmOrID), autoResumeGlyph)
	}
	return sb.String()
}
