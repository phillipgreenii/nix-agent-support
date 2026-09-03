package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
)

// TestDetect_RefusesOnlyNoTTY: a plain *bytes.Buffer never satisfies the
// term.File interface colorprofile.Detect uses to check isatty, so it always
// reports NoTTY regardless of the ambient environment — this is the "not a
// terminal at all" case Binding Decision 5 refuses on.
func TestDetect_RefusesOnlyNoTTY(t *testing.T) {
	var buf bytes.Buffer
	theme, err := Detect(&buf)
	if err == nil {
		t.Fatal("expected an error for a NoTTY writer, got nil")
	}
	if !strings.Contains(err.Error(), colorprofile.NoTTY.String()) {
		t.Errorf("error must name the profile %q; got %q", colorprofile.NoTTY.String(), err.Error())
	}
	if theme.OK.GetForeground() != (lipgloss.NoColor{}) || theme.Cursor.GetBold() {
		t.Errorf("expected a zero-value Theme on refusal, got %+v", theme)
	}
}

// TestDetect_AsciiForcedSucceedsWithMonoTheme forces colorprofile's own
// TTY_FORCE test seam (isTTYForced) so isatty reads true without a real
// terminal, plus NO_COLOR so the resolved profile clamps down to Ascii — a
// real terminal with no color support — rather than NoTTY. Ascii must NOT
// refuse (Binding Decision 5); it must return the mono theme branch.
func TestDetect_AsciiForcedSucceedsWithMonoTheme(t *testing.T) {
	t.Setenv("TTY_FORCE", "1")
	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	theme, err := Detect(&buf)
	if err != nil {
		t.Fatalf("Ascii profile must not refuse: %v", err)
	}
	// Ascii forces the mono theme branch: health-axis foregrounds stay unset,
	// while structural styles (Cursor/ActiveToggle) are still set.
	if theme.OK.GetForeground() != (lipgloss.NoColor{}) {
		t.Errorf("Ascii-forced Detect must leave OK's foreground unset, got %+v", theme.OK.GetForeground())
	}
	if !theme.Cursor.GetBold() {
		t.Error("Ascii-forced Detect must still bold Cursor (structural, not a health color)")
	}
}

func TestNewTheme_ColorBranchSetsHealthForegrounds(t *testing.T) {
	theme := NewTheme(true)
	if theme.OK.GetForeground() == (lipgloss.NoColor{}) {
		t.Error("color theme OK must set a foreground")
	}
	if theme.Failing.GetForeground() == (lipgloss.NoColor{}) {
		t.Error("color theme Failing must set a foreground")
	}
	if !theme.Failing.GetBold() {
		t.Error("color theme Failing must be bold")
	}
}

func TestNewTheme_MonoBranchLeavesHealthStylesUnset(t *testing.T) {
	theme := NewTheme(false)
	if theme.OK.GetForeground() != (lipgloss.NoColor{}) {
		t.Error("mono theme OK must have no foreground")
	}
	if theme.Failing.GetForeground() != (lipgloss.NoColor{}) {
		t.Error("mono theme Failing must have no foreground")
	}
	if !theme.Cursor.GetBold() {
		t.Error("mono theme Cursor must still be bold (structural, not a health color)")
	}
	if !theme.ActiveToggle.GetUnderline() {
		t.Error("mono theme ActiveToggle must still be underlined (structural, not a health color)")
	}
}
