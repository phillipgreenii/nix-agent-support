package tui

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestEmptyState_ThreeWayPrecedence is this packet's own acceptance bar:
// empty-state precedence is unknown > suppressed > empty; config-derived
// panes stay populated-and-dimmed while paused, never suppressed
// [design: Task 4.6 Step 6; ux-9].
func TestEmptyState_ThreeWayPrecedence(t *testing.T) {
	cases := []struct {
		name                       string
		unknown, suppressed, empty bool
		want                       emptyState
	}{
		{"unknown wins over suppressed and empty", true, true, true, stateUnknown},
		{"suppressed wins over empty", false, true, true, stateSuppressed},
		{"empty when nothing higher applies", false, false, true, stateEmpty},
		{"not empty when nothing applies", false, false, false, stateNotEmpty},
	}
	for _, c := range cases {
		if got := resolveEmptyState(c.unknown, c.suppressed, c.empty); got != c.want {
			t.Errorf("%s: resolveEmptyState = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEmptyStateText_RendersFixedWordingExceptForEmpty(t *testing.T) {
	if got, want := emptyStateText(stateUnknown, "custom"), "loading…"; got != want {
		t.Errorf("stateUnknown text = %q, want %q", got, want)
	}
	if got := emptyStateText(stateSuppressed, "custom"); got == "custom" || got == "" {
		t.Errorf("stateSuppressed text = %q, want a fixed suppressed wording, not the pane's own emptyMsg", got)
	}
	if got, want := emptyStateText(stateEmpty, "custom"), "custom"; got != want {
		t.Errorf("stateEmpty text = %q, want the pane's own emptyMsg %q", got, want)
	}
	if got, want := emptyStateText(stateNotEmpty, "custom"), ""; got != want {
		t.Errorf("stateNotEmpty text = %q, want %q", got, want)
	}
}

// TestDimIfPaused_NeverSuppressesConfigDerivedContent pins the other half
// of the design's own rule: a config-derived pane's content survives
// verbatim (just wrapped in a muted style) while paused -- it is never
// replaced by a suppressed placeholder. Compared against theme.Muted's own
// Render call (not a raw string), for the same reason render/theme_test.go
// avoids raw-string ANSI assertions: Render()'s literal escape-code output
// depends on the ambient color-profile detection of the test process.
func TestDimIfPaused_NeverSuppressesConfigDerivedContent(t *testing.T) {
	theme := render.NewTheme(true)
	content := "reviewer  ok  14  0"

	if got := dimIfPaused(content, false, theme); got != content {
		t.Errorf("dimIfPaused(paused=false) = %q, want the content unchanged", got)
	}

	got := dimIfPaused(content, true, theme)
	if want := theme.Muted.Render(content); got != want {
		t.Errorf("dimIfPaused(paused=true) = %q, want the Muted-wrapped content %q", got, want)
	}
	// The original text must still be present verbatim -- dimming wraps,
	// it does not replace or truncate.
	if !strings.Contains(got, "reviewer") || !strings.Contains(got, "14") {
		t.Errorf("dimIfPaused(paused=true) lost content; got %q", got)
	}
}
