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
	row := renderSession(sv, opts, "└─", " ", false)
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
	row := renderSession(sv, opts, "└─", " ", false)
	localTime := resetsAt.Local().Format("15:04")
	if !strings.Contains(row, localTime) {
		t.Errorf("row missing reset time %q:\n%s", localTime, row)
	}
}

func TestHeaderCountdownVisible(t *testing.T) {
	now := time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC)
	resetsAt := now.Add(10 * time.Minute)
	tree := &aggregate.Tree{}
	hdr := Header(tree, HeaderOpts{
		AutoResume:      true,
		WindowResetsAt:  resetsAt,
		AutoResumeDelay: 45 * time.Second,
		Now:             now,
	})
	if !strings.Contains(hdr, "⏸") {
		t.Errorf("header missing ⏸ when autoResume on and window pending:\n%s", hdr)
	}
	if !strings.Contains(hdr, "resuming in") {
		t.Errorf("header missing countdown text:\n%s", hdr)
	}
}

func TestHeaderCountdownHiddenWhenAutoResumeOff(t *testing.T) {
	now := time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC)
	resetsAt := now.Add(10 * time.Minute)
	tree := &aggregate.Tree{}
	hdr := Header(tree, HeaderOpts{
		AutoResume:     false,
		WindowResetsAt: resetsAt,
		Now:            now,
	})
	if strings.Contains(hdr, "resuming in") {
		t.Errorf("header should not show countdown when autoResume off:\n%s", hdr)
	}
}
