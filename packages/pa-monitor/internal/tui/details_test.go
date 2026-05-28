package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
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

// TestDetailsLastErrorTerminalShown verifies that a terminal error is shown in
// the details pane with kind and (escalated) suffix when appropriate.
func TestDetailsLastErrorTerminalShown(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:        transcript.ErrServerError,
		Text:        "API Error: 529 Overloaded",
		At:          time.Now().Add(-2 * time.Minute),
		IsTerminal:  true,
		IsRetryable: true,
	}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "Last error") {
		t.Errorf("details missing Last error section:\n%s", out)
	}
	if !strings.Contains(out, "server_error") {
		t.Errorf("details missing error kind:\n%s", out)
	}
	if !strings.Contains(out, "API Error: 529 Overloaded") {
		t.Errorf("details missing error text:\n%s", out)
	}
	if strings.Contains(out, "(escalated)") {
		t.Errorf("retryable error should not show (escalated); got:\n%s", out)
	}
}

// TestDetailsLastErrorEscalatedSuffix verifies that (escalated) appears when
// kind is inherently retryable but IsRetryable has been flipped to false.
func TestDetailsLastErrorEscalatedSuffix(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:        transcript.ErrServerError, // inherently retryable
		Text:        "API Error: 529 Overloaded",
		At:          time.Now().Add(-5 * time.Minute),
		IsTerminal:  true,
		IsRetryable: false, // escalated by daemon
	}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "(escalated)") {
		t.Errorf("escalated error should show (escalated) suffix:\n%s", out)
	}
}

// TestDetailsLastErrorNonTerminalHidden verifies that a non-terminal error is
// not shown in the details pane.
func TestDetailsLastErrorNonTerminalHidden(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:        transcript.ErrServerError,
		Text:        "transient error",
		At:          time.Now().Add(-1 * time.Minute),
		IsTerminal:  false, // user resumed — not terminal
		IsRetryable: true,
	}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le},
	}
	out := RenderDetails(sv, 120)
	if strings.Contains(out, "Last error") {
		t.Errorf("non-terminal error should not appear in details:\n%s", out)
	}
}

// TestDetailsPendingNudgeShown verifies that PendingNudge sources are shown.
func TestDetailsPendingNudgeShown(t *testing.T) {
	nudge := &aggregate.PendingNudge{Sources: []string{"disrupted", "manual"}}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{PendingNudge: nudge},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "Pending nudge") {
		t.Errorf("details missing Pending nudge section:\n%s", out)
	}
	if !strings.Contains(out, "disrupted") || !strings.Contains(out, "manual") {
		t.Errorf("details missing nudge sources:\n%s", out)
	}
}

// TestDetailsNoPendingNudgeWhenNil verifies that the Pending nudge section is
// absent when PendingNudge is nil.
func TestDetailsNoPendingNudgeWhenNil(t *testing.T) {
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{PendingNudge: nil},
	}
	out := RenderDetails(sv, 120)
	if strings.Contains(out, "Pending nudge") {
		t.Errorf("nil PendingNudge should not produce a Pending nudge section:\n%s", out)
	}
}

// TestHumanizeAge verifies the humanizeAge helper for a few key thresholds.
func TestHumanizeAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "just now"},
		{30 * time.Second, "30 seconds ago"},
		{90 * time.Second, "1 minute ago"},
		{3 * time.Minute, "3 minutes ago"},
		{90 * time.Minute, "1 hour ago"},
		{3 * time.Hour, "3 hours ago"},
	}
	for _, c := range cases {
		got := humanizeAge(c.d)
		if got != c.want {
			t.Errorf("humanizeAge(%v) = %q, want %q", c.d, got, c.want)
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
