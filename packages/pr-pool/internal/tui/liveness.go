// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.6) carries liveness signalling (ux-2): the always-visible
// connection dot, the last-successful-poll clock, the droppable attention
// line, and the poll-error zone -- suppressed specifically on an ErrBusy
// (exit-9) poll failure, since that names a momentarily-saturated core, not
// an actual failure [design: Task 4.6 Files (liveness.go)].
package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// connectionDot renders the always-visible liveness indicator: ok-styled
// while the last successful poll (asOf) is fresh relative to pollInterval,
// stale-styled otherwise, disabled-styled pre-first-poll (asOf is zero).
// A frozen dashboard must never be silently indistinguishable from a live
// one [design: Task 4.6 (§7 Liveness signalling)].
func connectionDot(asOf, now time.Time, pollInterval time.Duration, theme render.Theme) string {
	if asOf.IsZero() {
		return theme.Disabled.Render("●")
	}
	threshold := pollInterval * 3
	if threshold <= 0 {
		threshold = 5 * time.Second
	}
	if now.Sub(asOf) > threshold {
		return theme.Stale.Render("●")
	}
	return theme.OK.Render("●")
}

// lastPollClock renders the footer's "last poll N.Ns ago" text (the
// design's own mockups: "last poll 0.4s ago"), or a placeholder
// pre-first-poll.
func lastPollClock(asOf, now time.Time) string {
	if asOf.IsZero() {
		return "last poll -"
	}
	d := now.Sub(asOf)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("last poll %.1fs ago", d.Seconds())
}

// attentionLine surfaces droppable, non-ok facts above the panes (dropOrder
// 1 -- the FIRST zone to drop under height pressure): a version mismatch
// between this TUI build and the core's reported version, and an UNMATCHED
// marker when the reply's unmatchedBindings is non-empty [design: Task 4.6
// Files (liveness.go); §5 (empty-state precedence's UNMATCHED marker); §8
// (version hint)]. Returns "" when nothing warrants a line -- the caller
// omits the zone entirely rather than adding an empty one.
func attentionLine(reply StatusReply, clientVersion string, theme render.Theme) string {
	var hints []string
	if reply.Core.Version != "" && clientVersion != "" && clientVersion != "dev" &&
		reply.Core.Version != clientVersion {
		hints = append(hints, "core version differs from this TUI build — restart after a rebuild if outdated")
	}
	if n := len(reply.UnmatchedBindings); n > 0 {
		hints = append(hints, fmt.Sprintf("UNMATCHED: %d binding(s) matched no configured role", n))
	}
	if len(hints) == 0 {
		return ""
	}
	return theme.Cooling.Render("! " + strings.Join(hints, " · "))
}

// pollErrorZone renders the red poll-error line (dropOrder 2) for a
// flagged, non-ErrBusy poll failure. ErrBusy (exit-9, ErrCallInFlight's
// core-side counterpart -- the core is momentarily saturated, not actually
// failing) is deliberately suppressed here: staleness is surfaced by the
// connection dot / last-poll clock alone for that outcome [design: Task
// 4.6 Files (liveness.go); Task 4.6 Acceptance Criteria].
func pollErrorZone(flagged bool, err error, theme render.Theme) string {
	if !flagged || err == nil || errors.Is(err, ErrBusy) {
		return ""
	}
	return theme.Failing.Render("poll error: " + textsafe.Sanitize(err.Error()))
}

// justifyFooter places left at the start of a `width`-wide line and right
// flush against its end, with at least one space of separation. left/right
// may carry ANSI styling; lipgloss.Width measures visible width only.
func justifyFooter(left, right string, width int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}
