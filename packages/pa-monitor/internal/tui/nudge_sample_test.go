package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestNudgeSampleOutputs is a documentation-style test that prints the
// rendered details panel for the three scenarios called out in the bead:
// (a) pending nudge, (b) recent-but-not-pending, (c) neither. Useful for
// human-reading the rendered shape and snapshotting in the PR.
func TestNudgeSampleOutputs(t *testing.T) {
	base := &session.Session{
		SessionID:    "abc123de",
		Name:         "feature-x",
		PID:          4242,
		TerminalHost: "tmux",
		Cwd:          "/Users/example/repo",
		Kind:         "interactive",
	}
	enrich := aggregate.SessionEnrichment{
		Model:         "claude-opus-4-7",
		ContextTokens: 25_000,
		FirstPrompt:   "Wire up the nudge feedback indicator.",
	}

	t.Run("pending", func(t *testing.T) {
		e := enrich
		e.PendingNudge = &aggregate.PendingNudge{Sources: []string{"manual"}}
		sv := &aggregate.SessionView{Session: base, SessionEnrichment: e}
		out := RenderDetails(sv, 80)
		t.Logf("--- details (pending) ---\n%s\n--- end ---", out)
		if !strings.Contains(out, "pending:") {
			t.Errorf("missing pending line")
		}
	})

	t.Run("recent_not_pending", func(t *testing.T) {
		e := enrich
		e.LastNudgedAt = time.Now().Add(-90 * time.Second)
		e.LastNudgeSources = []string{"disrupted"}
		sv := &aggregate.SessionView{Session: base, SessionEnrichment: e}
		out := RenderDetails(sv, 80)
		t.Logf("--- details (recent) ---\n%s\n--- end ---", out)
		if !strings.Contains(out, "last sent:") {
			t.Errorf("missing last sent line")
		}
	})

	t.Run("neither", func(t *testing.T) {
		sv := &aggregate.SessionView{Session: base, SessionEnrichment: enrich}
		out := RenderDetails(sv, 80)
		t.Logf("--- details (neither) ---\n%s\n--- end ---", out)
		if strings.Contains(out, "Nudge:") {
			t.Errorf("expected no Nudge: block")
		}
	})
}
