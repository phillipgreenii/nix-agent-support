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
			wantAll:  []string{"[C] ●", "tokens", "cost", "active", "all", "name", "id", "[R] ●", "[M] now", "[?]", "[q]"},
			wantNone: []string{"[C]●", "[t]tok"},
		},
		{
			name:     "narrow",
			width:    100,
			wantAll:  []string{"[C]●", "[t] tok", "cost", "[a] act", "all", "[n] nm", "id", "[R]●", "[M]now", "[?]", "[q]"},
			wantNone: []string{"[C] ●", "tokens", "active", "[M] now"},
		},
		{
			name:     "tiny",
			width:    60,
			wantAll:  []string{"[C]●", "[t]tok", "[a]act", "[n]nm", "[R]●", "[M]now", "[?]", "[q]"},
			wantNone: []string{"[C] ●", "[C]affeinate", "tokens", "active", "[M] now", "tok · cost"},
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
