package render

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestControlsTierContent(t *testing.T) {
	cases := []struct {
		name     string
		width    int
		wantAll  []string
		wantNone []string
	}{
		{
			name:     "wide",
			width:    140,
			wantAll:  []string{"Caffeinated Enabled", "tokens", "cost", "active", "all", "name", "id", "Auto Nudge Enabled", "[N] nudge", "[?]", "[q]"},
			wantNone: []string{"[C]●", "[C] ●", "[t]tok"},
		},
		{
			name:     "narrow",
			width:    100,
			wantAll:  []string{"[C]●", "[t] tok", "cost", "[a] act", "all", "[n] nm", "id", "[R]●", "[N]nudge", "[?]", "[q]"},
			wantNone: []string{"[C] ●", "tokens", "active", "[N] nudge"},
		},
		{
			name:     "tiny",
			width:    60,
			wantAll:  []string{"[C]●", "[t]tok", "[a]act", "[n]nm", "[R]●", "[N]nudge", "[?]", "[q]"},
			wantNone: []string{"[C] ●", "[C]affeinate", "tokens", "active", "[N] nudge", "tok · cost"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Controls(ControlsOpts{Width: tc.width, CaffeinateOn: true, AutoResume: true})
			for _, want := range tc.wantAll {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in controls (tier=%s):\n%s", want, tc.name, out)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(out, none) {
					t.Errorf("should not contain %q (tier=%s):\n%s", none, tc.name, out)
				}
			}
			if strings.Contains(out, "\n") {
				t.Errorf("controls must be single line, got newlines:\n%s", out)
			}
		})
	}
}

func TestControlsFitsAtTierFloor(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		got := Controls(ControlsOpts{Width: w})
		if width := lipgloss.Width(got); width > w {
			t.Errorf("Controls(%d) width = %d, want <= %d; got %q", w, width, w, got)
		}
	}
}

func TestControlsCaffeineGraceCountdown(t *testing.T) {
	out := Controls(ControlsOpts{Width: 200, CaffeinateOn: true, GraceRemaining: 55 * time.Second})
	if !strings.Contains(out, "55s") {
		t.Errorf("expected grace countdown '55s' at WIDE, got:\n%s", out)
	}
	tiny := Controls(ControlsOpts{Width: 60, CaffeinateOn: true, GraceRemaining: 55 * time.Second})
	if strings.Contains(tiny, "55s") {
		t.Errorf("grace should drop at TINY, got:\n%s", tiny)
	}
}

// TestControlsDaemonConnectedIndicator verifies that the daemon connection
// status is rendered as a glyph at the start of the controls row, distinct
// between the connected and disconnected cases, at every tier (including
// TierTiny). The indicator must always be present so the user can see at a
// glance whether the daemon RPC is alive.
func TestControlsDaemonConnectedIndicator(t *testing.T) {
	for _, w := range []int{60, 100, 140} {
		connected := Controls(ControlsOpts{Width: w, DaemonConnected: true})
		offline := Controls(ControlsOpts{Width: w, DaemonConnected: false})
		if !strings.HasPrefix(lipgloss.NewStyle().Render(connected), "") {
			// no-op; placeholder for clarity
		}
		if connected == offline {
			t.Errorf("width=%d: connected and offline controls must differ; both =\n%s", w, connected)
		}
		// Strip ANSI to compare raw glyph content.
		connRaw := stripANSI(connected)
		offRaw := stripANSI(offline)
		// Connected indicator: filled circle as first non-space token.
		if !strings.HasPrefix(connRaw, "●") {
			t.Errorf("width=%d: connected controls must start with '●', got %q", w, connRaw)
		}
		// Offline indicator: hollow circle as first non-space token.
		if !strings.HasPrefix(offRaw, "○") {
			t.Errorf("width=%d: offline controls must start with '○', got %q", w, offRaw)
		}
	}
}

// TestControlsDaemonIndicatorFitsAtTierFloor verifies the indicator doesn't
// push the controls row past the width budget at any tier.
func TestControlsDaemonIndicatorFitsAtTierFloor(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		for _, connected := range []bool{true, false} {
			got := Controls(ControlsOpts{Width: w, DaemonConnected: connected})
			if width := lipgloss.Width(got); width > w {
				t.Errorf("Controls(width=%d, connected=%v) width=%d, want <= %d; got %q", w, connected, width, w, got)
			}
		}
	}
}

// stripANSI removes ANSI escape sequences so tests can check raw glyph content
// without depending on the colour profile.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
