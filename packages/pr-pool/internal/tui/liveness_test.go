package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestConnectionDot_FreshVsStaleVsUnknown checks which THEME STYLE
// connectionDot delegates to, not the rendered string directly -- like
// render/theme_test.go's own convention, a lipgloss Render() call's actual
// escape-code output depends on the ambient/detected color profile of the
// process running the test, so two color-only styles can render
// byte-identical text in a colorless test environment even though the
// STYLES themselves differ. Comparing against the theme's own Render call
// keeps the assertion meaningful regardless of that environment.
func TestConnectionDot_FreshVsStaleVsUnknown(t *testing.T) {
	theme := render.NewTheme(true)
	now := time.Now()

	if got, want := connectionDot(time.Time{}, now, time.Second, theme), theme.Disabled.Render("●"); got != want {
		t.Errorf("connectionDot pre-first-poll = %q, want the Disabled-styled dot %q", got, want)
	}
	if got, want := connectionDot(now.Add(-100*time.Millisecond), now, time.Second, theme), theme.OK.Render("●"); got != want {
		t.Errorf("connectionDot fresh = %q, want the OK-styled dot %q", got, want)
	}
	if got, want := connectionDot(now.Add(-1*time.Hour), now, time.Second, theme), theme.Stale.Render("●"); got != want {
		t.Errorf("connectionDot stale = %q, want the Stale-styled dot %q", got, want)
	}
}

func TestLastPollClock_FormatsSecondsAgo(t *testing.T) {
	now := time.Now()
	if got := lastPollClock(time.Time{}, now); !strings.Contains(got, "-") {
		t.Errorf("lastPollClock pre-first-poll = %q, want a placeholder", got)
	}
	got := lastPollClock(now.Add(-400*time.Millisecond), now)
	if !strings.Contains(got, "0.4s ago") {
		t.Errorf("lastPollClock = %q, want it to contain %q", got, "0.4s ago")
	}
}

func TestAttentionLine_VersionMismatchAndUnmatchedBindings(t *testing.T) {
	theme := render.NewTheme(false)

	if got := attentionLine(StatusReply{}, "dev", theme); got != "" {
		t.Errorf("attentionLine with nothing to flag = %q, want \"\"", got)
	}
	if got := attentionLine(StatusReply{Core: CoreInfo{Version: "1.0.0"}}, "1.0.1", theme); !strings.Contains(got, "core version differs") {
		t.Errorf("attentionLine version mismatch = %q, want the mismatch hint", got)
	}
	if got := attentionLine(StatusReply{UnmatchedBindings: []string{"bead.new"}}, "dev", theme); !strings.Contains(got, "UNMATCHED") {
		t.Errorf("attentionLine unmatched bindings = %q, want the UNMATCHED marker", got)
	}
}

func TestPollErrorZone_SuppressedOnErrBusy(t *testing.T) {
	theme := render.NewTheme(false)

	if got := pollErrorZone(true, ErrBusy, theme); got != "" {
		t.Errorf("pollErrorZone(ErrBusy) = %q, want \"\" (suppressed)", got)
	}
	if got := pollErrorZone(false, errors.New("boom"), theme); got != "" {
		t.Errorf("pollErrorZone(flagged=false) = %q, want \"\"", got)
	}
	if got := pollErrorZone(true, errors.New("boom"), theme); !strings.Contains(got, "boom") {
		t.Errorf("pollErrorZone(other error) = %q, want it to carry the error text", got)
	}
	wrapped := errors.New("wrapped: " + core.ErrNoRunningCore.Error())
	if got := pollErrorZone(true, wrapped, theme); got == "" {
		t.Errorf("pollErrorZone should still render for a non-ErrBusy error (no-core is handled elsewhere, via the screen transition, not by suppressing this zone)")
	}
}

func TestJustifyFooter_PlacesRightFlushWithGap(t *testing.T) {
	got := justifyFooter("left", "right", 20)
	if !strings.HasPrefix(got, "left") {
		t.Errorf("justifyFooter = %q, want it to start with %q", got, "left")
	}
	if !strings.HasSuffix(got, "right") {
		t.Errorf("justifyFooter = %q, want it to end with %q", got, "right")
	}
	// Overlong content still separates by at least one space rather than
	// silently concatenating.
	over := justifyFooter(strings.Repeat("x", 30), "right", 20)
	if strings.Contains(over, "xright") {
		t.Errorf("justifyFooter dropped the separating space under overflow; got %q", over)
	}
}
