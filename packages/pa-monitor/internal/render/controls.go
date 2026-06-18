package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
)

// CaffeinateProcess is the caffeination PROCESS state for display — what the
// wake-assertion subprocess is actually doing, orthogonal to the MODE toggle
// (CaffeinateOn). The incident state was MODE on + PROCESS off; rendering both
// makes that distinct.
type CaffeinateProcess int

const (
	CaffeinateProcessOff   CaffeinateProcess = iota // not holding the assertion
	CaffeinateProcessOn                             // holding the assertion
	CaffeinateProcessGrace                          // armed countdown (see GraceRemaining)
	CaffeinateProcessError                          // spawn failed
)

// ControlsOpts carries everything Controls needs to render the toggle row.
type ControlsOpts struct {
	// CaffeinateOn is the auto-caffeinate MODE (the user toggle).
	CaffeinateOn bool
	// CaffeinateProcess is the caffeination PROCESS state. Rendered as a second
	// marker alongside the MODE so "armed but not holding" (the incident) and
	// "spawn failed" are visibly distinct from "holding".
	CaffeinateProcess CaffeinateProcess
	GraceRemaining    time.Duration
	ShowAll           bool
	CostMode          bool
	ForceID           bool
	AutoResume        bool
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
//	WIDE   ≥120  ● [C] ● on  [t] tokens · cost  [a] active · all  [n] name · id  [R] ● on  [N] nudge  [?]  [q]
//	NARROW 80–119 ● [C]●  [t] tok · cost  [a] act · all  [n] nm · id  [R]●  [N]nudge  [?][q]
//	TINY   <80   ● [C]●  [t]tok  [a]act  [n]nm  [R]●  [N]nudge  [?][q]
//
// The leading glyph is the daemon RPC connection indicator: filled "●" when
// the latest poll succeeded, hollow "○" when offline. The active half of
// each toggle is highlighted via theme.ActiveToggle. At TINY only the active
// half of each toggle is shown.
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

	// Two caffeinate indicators (D6): MODE (the user toggle) and PROCESS (what
	// the wake-assertion subprocess is doing). MODE answers "did the user arm
	// auto-caffeinate"; PROCESS answers "is the Mac actually being held awake".
	//
	// WIDE renders both as "mode · process"; NARROW/TINY render a MODE glyph
	// plus a one-char PROCESS marker so "armed but not holding" (the incident:
	// mode on, process off) and "spawn failed" stay distinguishable.
	caffProc := caffeinateProcessLabel(opts.CaffeinateProcess, opts.GraceRemaining)
	caffProcMark := caffeinateProcessMark(opts.CaffeinateProcess)

	var caffWide, caffGlyph string
	if opts.CaffeinateOn {
		modeGlyph := th.ActiveToggle.Render("●")
		caffWide = th.ActiveToggle.Render("● on") + " · " + caffProc
		caffGlyph = modeGlyph + caffProcMark
	} else {
		caffWide = "○ off · " + caffProc
		caffGlyph = "○" + caffProcMark
	}

	autoResumeWide := "○ off"
	autoResumeGlyph := "○"
	if opts.AutoResume {
		autoResumeWide = th.ActiveToggle.Render("● on")
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
		fmt.Fprintf(&sb, "[C] %s  [t] %s · %s  [a] %s · %s  [n] %s · %s  [R] %s  [N] nudge  [?]  [q]",
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

// caffeinateProcessLabel returns the word form of the PROCESS state for the
// WIDE controls row. The grace state carries its remaining seconds, which the
// daemon now feeds through (previously this branch was dead because the TUI
// never supplied GraceRemaining).
func caffeinateProcessLabel(p CaffeinateProcess, graceRemaining time.Duration) string {
	switch p {
	case CaffeinateProcessOn:
		return "holding"
	case CaffeinateProcessGrace:
		if graceRemaining > 0 {
			return fmt.Sprintf("grace %ds", int(graceRemaining.Seconds()))
		}
		return "grace"
	case CaffeinateProcessError:
		return "ERROR"
	default:
		return "off"
	}
}

// caffeinateProcessMark returns the compact one-char PROCESS marker for the
// NARROW/TINY tiers: holding "*", grace "~", error "!", off "" (nothing).
func caffeinateProcessMark(p CaffeinateProcess) string {
	switch p {
	case CaffeinateProcessOn:
		return "*"
	case CaffeinateProcessGrace:
		return "~"
	case CaffeinateProcessError:
		return "!"
	default:
		return ""
	}
}
