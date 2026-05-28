package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/models"
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
	sb.WriteString(fmt.Sprintf("Name:      %s\n", wrap.Line(sv.Name, valBudget)))
	sb.WriteString(fmt.Sprintf("ID:        %s\n", wrap.Line(sv.SessionID, valBudget)))
	sb.WriteString(fmt.Sprintf("PID:       %d\n", sv.PID))
	sb.WriteString(fmt.Sprintf("Terminal:  %s\n", wrap.Line(sv.TerminalHost, valBudget)))
	sb.WriteString(fmt.Sprintf("Cwd:       %s\n", wrap.Line(sv.Cwd, valBudget)))
	sb.WriteString(fmt.Sprintf("Kind:      %s\n", wrap.Line(string(sv.Kind), valBudget)))
	sb.WriteString(fmt.Sprintf("Model:     %s\n", wrap.Line(sv.SessionEnrichment.Model, valBudget)))
	win, _ := models.Window(sv.SessionEnrichment.Model)
	ctxPct := 0.0
	if win > 0 {
		ctxPct = 100 * float64(sv.SessionEnrichment.ContextTokens) / float64(win)
	}
	sb.WriteString(fmt.Sprintf("Context:   %s / %s tokens (%.0f%%)\n",
		render.FmtTok(sv.SessionEnrichment.ContextTokens), render.FmtTok(win), ctxPct))
	sb.WriteString(fmt.Sprintf("Subagents: %d\n", sv.SessionEnrichment.SubagentCount))
	sb.WriteString(fmt.Sprintf("Subshells: %d\n", sv.SessionEnrichment.SubshellCount))
	sb.WriteString("\nFirst prompt:\n")
	for line := range strings.SplitSeq(sv.SessionEnrichment.FirstPrompt, "\n") {
		sb.WriteString(wrap.Line(line, ew))
		sb.WriteString("\n")
	}

	// Last error section: only shown when the error is terminal.
	le := sv.SessionEnrichment.LastError
	if le != nil && le.IsTerminal {
		kindStr := string(le.Kind)
		if isEscalated(le) {
			kindStr += "  (escalated)"
		}
		sb.WriteString(fmt.Sprintf("\nLast error:  %s\n", kindStr))
		errText := le.Text
		if len(errText) > 200 {
			errText = errText[:200] + "…"
		}
		if errText != "" {
			sb.WriteString(fmt.Sprintf("             %s\n", wrap.Line(errText, valBudget)))
		}
		sb.WriteString(fmt.Sprintf("             %s\n", humanizeAge(time.Since(le.At))))
	}

	// Pending nudge section.
	if sv.SessionEnrichment.PendingNudge != nil && len(sv.SessionEnrichment.PendingNudge.Sources) > 0 {
		sb.WriteString(fmt.Sprintf("Pending nudge: [%s]\n", strings.Join(sv.SessionEnrichment.PendingNudge.Sources, ", ")))
	}

	sb.WriteString("\n[esc] close")
	return sb.String()
}

// isEscalated reports whether the error record was escalated: the kind is
// inherently retryable by spec (unknown or server_error) but IsRetryable has
// been flipped to false by the daemon's escalation logic.
func isEscalated(le *transcript.ErrorRecord) bool {
	return le.Kind.IsRetryable() && !le.IsRetryable
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
