package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestDetailsShowsTerminalHost(t *testing.T) {
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1", TerminalHost: "tmux"},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "tmux") {
		t.Errorf("details missing terminal host:\n%s", out)
	}
}

func TestDetailsOverlayShowsPerModelBreakdown(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Name: "n1", Cwd: "/p", PID: 42, Kind: "interactive"},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model: "claude-opus-4-7", ContextTokens: 5000,
			FirstPrompt: "first prompt text",
		},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "n1") || !strings.Contains(out, "id1") {
		t.Errorf("details missing identifiers:\n%s", out)
	}
	if !strings.Contains(out, "first prompt text") {
		t.Errorf("details missing first prompt:\n%s", out)
	}
}

// TestDetailsRuleLineScalesWithWidth verifies the rule line stretches to the
// effective width at every tier, with the centered "Session Details" label.
func TestDetailsRuleLineScalesWithWidth(t *testing.T) {
	sv := &aggregate.SessionView{
		Session:           &session.Session{},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	cases := []struct{ width int }{{60}, {80}, {120}, {200}}
	for _, c := range cases {
		out := RenderDetails(sv, c.width)
		first := strings.SplitN(out, "\n", 2)[0]
		if !strings.Contains(first, "Session Details") {
			t.Errorf("rule line missing label at width=%d: %q", c.width, first)
		}
		if w := lipgloss.Width(first); w != c.width {
			t.Errorf("rule line width = %d, want %d at width=%d: %q", w, c.width, c.width, first)
		}
	}
}

// TestDetailsClipsLongValues verifies that an oversized field value is
// truncated with an ellipsis at narrow widths.
func TestDetailsClipsLongValues(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "id",
			Name:      "n",
			Cwd:       strings.Repeat("/very-long-path-segment", 10),
		},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	out := RenderDetails(sv, 60)
	for line := range strings.SplitSeq(out, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("details line exceeds width=60 (got %d): %q", w, line)
		}
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis somewhere when long Cwd is clipped:\n%s", out)
	}
}
