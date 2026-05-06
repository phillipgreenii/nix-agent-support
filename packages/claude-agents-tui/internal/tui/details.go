package tui

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/models"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

func RenderDetails(sv *aggregate.SessionView) string {
	var sb strings.Builder
	sb.WriteString("── Session Details ──────────────────────────────────\n")
	sb.WriteString(fmt.Sprintf("Name:      %s\n", sv.Name))
	sb.WriteString(fmt.Sprintf("ID:        %s\n", sv.SessionID))
	sb.WriteString(fmt.Sprintf("PID:       %d\n", sv.PID))
	sb.WriteString(fmt.Sprintf("Terminal:  %s\n", sv.TerminalHost))
	sb.WriteString(fmt.Sprintf("Cwd:       %s\n", sv.Cwd))
	sb.WriteString(fmt.Sprintf("Kind:      %s\n", sv.Kind))
	sb.WriteString(fmt.Sprintf("Model:     %s\n", sv.SessionEnrichment.Model))
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
	sb.WriteString(sv.SessionEnrichment.FirstPrompt)
	sb.WriteString("\n\n[esc] close")
	return sb.String()
}
