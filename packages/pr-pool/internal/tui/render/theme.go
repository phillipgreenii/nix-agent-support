// Package render is internal/tui's pure, leaf rendering/theme package: theme
// detection, width-tier clamping, and modal composition. It has no bubbletea
// dependency and no dependency on any other pr-pool package — it reproduces
// packages/pa-monitor/internal/render's mechanics rather than reinventing
// them. Its sole consumer is internal/tui (Task 4.4 onward).
package render

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
)

// Theme holds the display styles internal/tui uses to render pr-pool's own
// health grammar (see glyphs.go) plus a handful of structural styles
// (selection cursor, row highlight, muted text, an active toggle). It
// mirrors pa-monitor's own Theme (packages/pa-monitor/internal/render/theme.go),
// renamed for pr-pool's listener/source health axes rather than pa-monitor's
// session states. Zero-value renders plain text.
type Theme struct {
	OK       lipgloss.Style
	Cooling  lipgloss.Style
	Failing  lipgloss.Style
	Paused   lipgloss.Style
	Disabled lipgloss.Style
	Excluded lipgloss.Style
	Stale    lipgloss.Style

	Cursor       lipgloss.Style
	Row          lipgloss.Style
	Muted        lipgloss.Style
	ActiveToggle lipgloss.Style
}

// Detect probes w's terminal capability and returns the Theme it warrants.
//
// It refuses ONLY when w is not a terminal at all (colorprofile.NoTTY) —
// bubbletea's alt-screen program cannot run there regardless of color
// support. Every other profile, including colorprofile.Ascii (a real
// terminal with no color support: TERM=dumb, NO_COLOR, or a monochrome
// emulator), succeeds and returns the mono theme branch — refusing there
// would reject every legitimate TERM=dumb/NO_COLOR session.
func Detect(w io.Writer) (Theme, error) {
	profile := colorprofile.Detect(w, os.Environ())
	if profile == colorprofile.NoTTY {
		return Theme{}, fmt.Errorf(
			"render: terminal profile %s — the TUI needs a real terminal; run \"pr-pool tui\" directly in an interactive shell, not piped, redirected, or under cron",
			profile,
		)
	}
	return NewTheme(profile != colorprofile.Ascii), nil
}

// NewTheme builds a Theme. hasColors is false for colorprofile.Ascii (and a
// caller may also pass false to force mono for any other reason); every
// other detected profile passes true. Color indices 0-15 are terminal
// palette slots.
func NewTheme(hasColors bool) Theme {
	bold := lipgloss.NewStyle().Bold(true)
	underline := lipgloss.NewStyle().Underline(true)

	if !hasColors {
		faint := lipgloss.NewStyle().Faint(true)
		return Theme{
			Stale:        faint,
			Cursor:       bold,
			Row:          bold,
			Muted:        faint,
			ActiveToggle: underline,
		}
	}
	return Theme{
		OK:       lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Cooling:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Failing:  lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		Paused:   lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		Disabled: lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Excluded: lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		Stale:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),

		Cursor:       bold,
		Row:          bold,
		Muted:        lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		ActiveToggle: underline,
	}
}
