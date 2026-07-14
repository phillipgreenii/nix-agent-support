package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/models"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/render"
	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
)

// detailsLabelCols is the visible width of every "Foo:       " label prefix,
// kept identical so values align in a column.
const detailsLabelCols = 11

func RenderDetails(sv *aggregate.SessionView, width int) string {
	ew := wrap.EffectiveWidth(width)
	valBudget := max(ew-detailsLabelCols, 1)
	var sb strings.Builder
	sb.WriteString(detailsRuleLine(ew) + "\n")
	fmt.Fprintf(&sb, "Name:      %s\n", wrap.Line(sv.Name, valBudget))
	fmt.Fprintf(&sb, "ID:        %s\n", wrap.Line(sv.SessionID, valBudget))
	fmt.Fprintf(&sb, "PID:       %d\n", sv.PID)
	fmt.Fprintf(&sb, "Terminal:  %s\n", wrap.Line(sv.TerminalHost, valBudget))
	fmt.Fprintf(&sb, "Cwd:       %s\n", wrap.Line(sv.Cwd, valBudget))
	fmt.Fprintf(&sb, "Kind:      %s\n", wrap.Line(string(sv.Kind), valBudget))
	// ADR 0024: show status, and qualify a blocked session with its blocker
	// ("blocked/usage_limit") so the reason a session is stuck is visible here,
	// matching the CLI status table.
	statusStr := sv.Status.String()
	if sv.Status == session.Blocked && sv.Blocker != session.NoBlocker {
		statusStr += "/" + sv.Blocker.String()
	}
	fmt.Fprintf(&sb, "Status:    %s\n", wrap.Line(statusStr, valBudget))
	fmt.Fprintf(&sb, "Model:     %s\n", wrap.Line(sv.Model, valBudget))
	win, _ := models.Window(sv.Model)
	ctxPct := 0.0
	if win > 0 {
		ctxPct = 100 * float64(sv.ContextTokens) / float64(win)
	}
	fmt.Fprintf(&sb, "Context:   %s / %s tokens (%.0f%%)\n",
		render.FmtTok(sv.ContextTokens), render.FmtTok(win), ctxPct)
	fmt.Fprintf(&sb, "Subagents: %d\n", sv.SubagentCount)
	fmt.Fprintf(&sb, "Subshells: %d\n", sv.SubshellCount)
	sb.WriteString("\nFirst prompt:\n")
	for line := range strings.SplitSeq(sv.FirstPrompt, "\n") {
		sb.WriteString(wrap.Line(line, ew))
		sb.WriteString("\n")
	}

	// Last error section: only shown when the error is terminal.
	le := sv.LastError
	if le != nil && le.IsTerminal {
		kindStr := string(le.Kind)
		if le.FromSubagent {
			kindStr += "  (in subagent)"
		}
		if isEscalated(le, sv.LastErrorRetryable) {
			kindStr += "  (escalated)"
		}
		fmt.Fprintf(&sb, "\nLast error:  %s\n", kindStr)
		errText := le.Text
		if len(errText) > 200 {
			errText = errText[:200] + "…"
		}
		if errText != "" {
			fmt.Fprintf(&sb, "             %s\n", wrap.Line(errText, valBudget))
		}
		fmt.Fprintf(&sb, "             %s\n", humanizeAge(time.Since(le.At)))
	}

	// Nudge section: shows pending intents (when queued) and most recent
	// fire (when the watermark has been populated). Either, both, or neither
	// can appear. The block is preceded by a blank line so it stands apart
	// from the error block above.
	pendingSources := nudgeSources(sv)
	hasLast := !sv.LastNudgedAt.IsZero()
	if len(pendingSources) > 0 || hasLast {
		sb.WriteString("\nNudge:\n")
		if len(pendingSources) > 0 {
			fmt.Fprintf(&sb, "  pending: [%s]\n", strings.Join(pendingSources, ", "))
		}
		if hasLast {
			ageStr := humanizeAge(time.Since(sv.LastNudgedAt))
			fmt.Fprintf(&sb, "  last sent: %s\n", ageStr)
			if len(sv.LastNudgeSources) > 0 {
				fmt.Fprintf(&sb, "  via: [%s]\n", strings.Join(sv.LastNudgeSources, ", "))
			}
		}
	}

	sb.WriteString("\n[esc] close")
	return sb.String()
}

// nudgeSources returns the currently-pending nudge sources, or nil when
// nothing is pending. Pulled out so RenderDetails can show "Nudge:" header
// only when at least one of the sub-fields applies.
func nudgeSources(sv *aggregate.SessionView) []string {
	if sv.PendingNudge == nil {
		return nil
	}
	return sv.PendingNudge.Sources
}

// RenderDetailsWindow renders a height-bounded viewport over the full
// details body, starting at `scrollOffset` content lines. It returns at most
// `height` "\n"-separated lines and prepends/appends "↑ N more" / "↓ N more"
// indicators when content extends beyond the viewport.
//
// height <= 0 or width <= 0 falls back to the unclipped RenderDetails output.
func RenderDetailsWindow(sv *aggregate.SessionView, width, height, scrollOffset int) string {
	full := RenderDetails(sv, width)
	if height <= 0 {
		return full
	}
	lines := strings.Split(full, "\n")
	if len(lines) <= height {
		return full
	}

	// Clamp scrollOffset to valid range.
	//
	// Note +1: when scrolled past 0, the algorithm below reserves one row for
	// the "↑ N more" header. Allowing scrollOffset up to len-height+1 lets the
	// final content line end exactly at len, which suppresses the "↓ N more"
	// footer and exposes every trailing line. Without the +1 the algorithm
	// always thinks there's "more below" and steals another row for the
	// indicator, hiding the bottom two lines.
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	maxOffset := len(lines) - height + 1
	if maxOffset < 0 {
		maxOffset = 0
	}
	if scrollOffset > maxOffset {
		scrollOffset = maxOffset
	}

	hasAbove := scrollOffset > 0
	// Reserve indicator lines from the available budget. The indicators take
	// the place of the topmost / bottommost visible lines.
	rowBudget := height
	if hasAbove {
		rowBudget--
	}
	if rowBudget < 1 {
		rowBudget = 1
	}
	end := scrollOffset + rowBudget
	if end > len(lines) {
		end = len(lines)
	}
	hasBelow := end < len(lines)
	if hasBelow {
		rowBudget--
		if rowBudget < 1 {
			rowBudget = 1
		}
		end = scrollOffset + rowBudget
		if end > len(lines) {
			end = len(lines)
		}
		hasBelow = end < len(lines)
	}

	var out []string
	if hasAbove {
		out = append(out, fmt.Sprintf("↑ %d more", scrollOffset))
	}
	out = append(out, lines[scrollOffset:end]...)
	if hasBelow {
		out = append(out, fmt.Sprintf("↓ %d more", len(lines)-end))
	}
	return strings.Join(out, "\n")
}

// isEscalated reports whether the error record was escalated: the record's
// class is inherently retryable by pa-monitor's policy (transient server or
// network) but the auto-resume verdict (retryable) has been flipped to false by
// the daemon's escalation logic.
func isEscalated(le *transcript.ErrorRecord, retryable bool) bool {
	return transcript.Retryable(le) && !retryable
}

// humanizeAge formats a duration as a human-readable age string like
// "just now", "30 seconds ago", "2 minutes ago", "1 hour ago".
func humanizeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < 2*time.Minute:
		return "1 minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "1 hour ago"
	default:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
}

// detailsRuleLine renders a width-exact rule like "── Session Details ──...──".
func detailsRuleLine(width int) string {
	label := " Session Details "
	leftDashes := 2
	labelW := lipgloss.Width(label)
	if width <= leftDashes+labelW {
		return label
	}
	rightDashes := width - leftDashes - labelW
	return strings.Repeat("─", leftDashes) + label + strings.Repeat("─", rightDashes)
}
