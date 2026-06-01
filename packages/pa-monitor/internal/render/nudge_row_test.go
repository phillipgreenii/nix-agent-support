package render

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// withFrozenNow swaps the package nowFn for the duration of the callback so
// tests can drive the "recent nudge" decay deterministically.
func withFrozenNow(t *testing.T, now time.Time, fn func()) {
	t.Helper()
	prev := nowFn
	nowFn = func() time.Time { return now }
	defer func() { nowFn = prev }()
	fn()
}

// TestSessionGlyphRecentNudgeMarker verifies that a session with a recent
// successful nudge fire (within recentNudgeWindow) but no pending intent
// gets the ✉ marker appended to the idle glyph.
func TestSessionGlyphRecentNudgeMarker(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastNudgedAt:     now.Add(-5 * time.Second),
			LastNudgeSources: []string{"manual"},
		},
	}
	withFrozenNow(t, now, func() {
		glyph := sessionGlyph(sv, Theme{})
		if !strings.Contains(glyph, "✉") {
			t.Errorf("recent nudge: expected ✉ marker; got %q", glyph)
		}
		if strings.Contains(glyph, "↪") {
			t.Errorf("no pending intent: ↪ should be absent; got %q", glyph)
		}
	})
}

// TestSessionGlyphRecentNudgeExpired verifies that a stale LastNudgedAt
// (older than recentNudgeWindow) does not produce a ✉ marker.
func TestSessionGlyphRecentNudgeExpired(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastNudgedAt: now.Add(-2 * recentNudgeWindow),
		},
	}
	withFrozenNow(t, now, func() {
		glyph := sessionGlyph(sv, Theme{})
		if strings.Contains(glyph, "✉") {
			t.Errorf("expired LastNudgedAt: ✉ should be absent; got %q", glyph)
		}
	})
}

// TestSessionGlyphPendingTakesPriorityOverRecent verifies that ↪ is shown when
// both pending and recent-fire would otherwise apply.
func TestSessionGlyphPendingTakesPriorityOverRecent(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			PendingNudge: &aggregate.PendingNudge{Sources: []string{"manual"}},
			LastNudgedAt: now.Add(-5 * time.Second),
		},
	}
	withFrozenNow(t, now, func() {
		glyph := sessionGlyph(sv, Theme{})
		if !strings.Contains(glyph, "↪") {
			t.Errorf("pending intent: expected ↪ marker; got %q", glyph)
		}
		if strings.Contains(glyph, "✉") {
			t.Errorf("pending intent: ✉ should not appear; got %q", glyph)
		}
	})
}

// TestSessionGlyphWorkingHidesNudgeMarkers verifies that a Working session
// suppresses both nudge markers — operator already knows the session is busy.
func TestSessionGlyphWorkingHidesNudgeMarkers(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{
			PendingNudge: &aggregate.PendingNudge{Sources: []string{"manual"}},
			LastNudgedAt: now.Add(-5 * time.Second),
		},
	}
	withFrozenNow(t, now, func() {
		glyph := sessionGlyph(sv, Theme{})
		if strings.Contains(glyph, "↪") || strings.Contains(glyph, "✉") {
			t.Errorf("Working session: nudge markers should be hidden; got %q", glyph)
		}
	})
}

// TestSessionRowMarkerWidthNeutralAcrossTiers verifies that adding the
// recent-fire ✉ marker does not change row width vs a no-marker baseline at
// any tier (TierTiny 60, TierNarrow 80, TierWide 120). Independent of whether
// the row already exceeds the tier width budget at minLabelWidth: the row
// width math is identical with or without the marker.
func TestSessionRowMarkerWidthNeutralAcrossTiers(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	baseline := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1", Name: "n1", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	withNudge := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Name: "n1", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastNudgedAt:     now.Add(-5 * time.Second),
			LastNudgeSources: []string{"manual"},
		},
	}
	withPending := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Name: "n1", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			PendingNudge: &aggregate.PendingNudge{Sources: []string{"disrupted", "manual"}},
		},
	}
	cases := []struct {
		name  string
		width int
	}{
		{"tiny", 60},
		{"narrow", 80},
		{"wide", 120},
	}
	withFrozenNow(t, now, func() {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				opts := TreeOpts{Width: tc.width, Theme: NewTheme(false)}
				base := strings.TrimRight(renderSession(baseline, opts, "└─", false), "\n")
				rec := strings.TrimRight(renderSession(withNudge, opts, "└─", false), "\n")
				pen := strings.TrimRight(renderSession(withPending, opts, "└─", false), "\n")
				bw := lipgloss.Width(base)
				rw := lipgloss.Width(rec)
				pw := lipgloss.Width(pen)
				if rw != bw {
					t.Errorf("recent-fire marker changed row width: baseline=%d with=%d at tier=%d", bw, rw, tc.width)
				}
				if pw != bw {
					t.Errorf("pending marker changed row width: baseline=%d with=%d at tier=%d", bw, pw, tc.width)
				}
				if !strings.Contains(rec, "✉") {
					t.Errorf("expected ✉ marker in row at width=%d:\n%q", tc.width, rec)
				}
				if !strings.Contains(pen, "↪") {
					t.Errorf("expected ↪ marker in row at width=%d:\n%q", tc.width, pen)
				}
			})
		}
	})
}
