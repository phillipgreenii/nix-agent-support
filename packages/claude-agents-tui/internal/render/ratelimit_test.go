// internal/render/ratelimit_test.go
package render

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestSessionRowShowsPauseGlyph(t *testing.T) {
	resetsAt := time.Date(2026, 5, 6, 3, 47, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "abc123", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			RateLimitResetsAt: resetsAt,
		},
	}
	opts := TreeOpts{Theme: NewTheme(false)}
	row := renderSession(sv, opts, "└─", false)
	if !strings.Contains(row, "⏸") {
		t.Errorf("row missing ⏸ glyph:\n%s", row)
	}
}

func TestSessionRowShowsResetTime(t *testing.T) {
	resetsAt := time.Date(2026, 5, 6, 3, 47, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "abc123", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			RateLimitResetsAt: resetsAt,
		},
	}
	opts := TreeOpts{Theme: NewTheme(false)}
	row := renderSession(sv, opts, "└─", false)
	localTime := resetsAt.Local().Format("15:04")
	if !strings.Contains(row, localTime) {
		t.Errorf("row missing reset time %q:\n%s", localTime, row)
	}
}

func TestAlertsCountdownVisible(t *testing.T) {
	now := time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC)
	resetsAt := now.Add(10 * time.Minute)
	tree := &aggregate.Tree{}
	out := Alerts(tree, AlertsOpts{
		AutoResume:      true,
		WindowResetsAt:  resetsAt,
		AutoResumeDelay: 45 * time.Second,
		Now:             now,
		Width:           200,
	})
	if !strings.Contains(out, "⏸") {
		t.Errorf("alerts missing ⏸ when autoResume on and window pending:\n%s", out)
	}
	if !strings.Contains(out, "resuming in") {
		t.Errorf("alerts missing countdown text:\n%s", out)
	}
}

func TestAlertsCountdownHiddenWhenAutoResumeOff(t *testing.T) {
	now := time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC)
	resetsAt := now.Add(10 * time.Minute)
	tree := &aggregate.Tree{}
	out := Alerts(tree, AlertsOpts{
		AutoResume:     false,
		WindowResetsAt: resetsAt,
		Now:            now,
		Width:          200,
	})
	if strings.Contains(out, "resuming in") {
		t.Errorf("alerts should not show countdown when autoResume off:\n%s", out)
	}
}
