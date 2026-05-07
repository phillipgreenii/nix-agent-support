package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/models"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
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
	sb.WriteString("\n[esc] close")
	return sb.String()
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
