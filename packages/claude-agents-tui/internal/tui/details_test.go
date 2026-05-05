package tui

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestDetailsOverlayShowsPerModelBreakdown(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Name: "n1", Cwd: "/p", PID: 42, Kind: "interactive"},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model: "claude-opus-4-7", ContextTokens: 5000,
			FirstPrompt: "first prompt text",
		},
	}
	out := RenderDetails(sv)
	if !strings.Contains(out, "n1") || !strings.Contains(out, "id1") {
		t.Errorf("details missing identifiers:\n%s", out)
	}
	if !strings.Contains(out, "first prompt text") {
		t.Errorf("details missing first prompt:\n%s", out)
	}
}
