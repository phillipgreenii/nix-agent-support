package render

import (
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
)

// Theme holds display styles for the TUI. Zero-value renders plain text.
type Theme struct {
	Working      lipgloss.Style
	Idle         lipgloss.Style
	Awaiting     lipgloss.Style
	Dormant      lipgloss.Style
	Cursor       lipgloss.Style
	DirRow       lipgloss.Style
	Branch       lipgloss.Style
	Prompt       lipgloss.Style
	ActiveToggle lipgloss.Style
}

// DetectColors returns true when the terminal supports ANSI color output.
func DetectColors() bool {
	p := colorprofile.Detect(os.Stdout, os.Environ())
	return p != colorprofile.NoTTY && p != colorprofile.Ascii
}

// NewTheme builds a Theme. Color indices 0–15 are terminal palette slots;
// Stylix maps them via base16 at the terminal level.
func NewTheme(hasColors bool) Theme {
	bold := lipgloss.NewStyle().Bold(true)
	underline := lipgloss.NewStyle().Underline(true)

	if !hasColors {
		faint := lipgloss.NewStyle().Faint(true)
		return Theme{
			Dormant:      faint,
			Cursor:       bold,
			DirRow:       bold,
			Branch:       faint,
			Prompt:       faint,
			ActiveToggle: underline,
		}
	}
	return Theme{
		Working:      lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Idle:         lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Awaiting:     lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		Dormant:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Cursor:       bold,
		DirRow:       bold,
		Branch:       lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Prompt:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		ActiveToggle: underline,
	}
}
