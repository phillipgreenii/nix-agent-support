package tui

import (
	"strings"

	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// noCoreMessage renders screenNoCore, reproducing pa-monitor's own
// daemonOfflineMessage shape (packages/pa-monitor/internal/tui/view.go's
// daemonOfflineMessage): the last error verbatim, the discovery path, both
// remedies, the auto-reconnect note, and a press-q line. Both remedies are
// ALWAYS shown as plain text, never conditioned on live systemd/launchd
// detection -- this package has no way to check that [design: Task 4.5
// Files].
//
// Sanitize-before-render ordering (Task 4.5 Step 5): err and discoveryPath
// are run through textsafe.Sanitize BEFORE they are woven into the message,
// never after -- a later packet's width measurement (Tasks 4.6/4.9) must
// never see a raw control sequence hiding in either value.
//
// Width clipping (pg2-wp7k6): the composed message is run through
// render.Block/render.EffectiveWidth before returning, matching the pattern
// every other tui render/pane file already uses (e.g. banner.go's
// renderHeader) -- the remedy line ("or supervise it as a long-running
// daemon (the pg-router-daemon service, if configured).", 85 columns)
// otherwise exceeds narrow widths unconditionally.
//
// Height fill (pg2-3ll1n): the width-clipped message is then padded to
// `height` lines via padOrExtend (zones.go) -- the SAME unexported helper
// screenMain's own layoutZones already uses to fill the terminal, rather
// than a new fullscreen mechanism invented for this one screen. Every
// other screen fills the terminal by construction: screenMain via
// layoutZones+padOrExtend, screenModal via render.Modal's
// lipgloss.Place(width, height, ...). screenNoCore's fixed-line message was
// the one screen left short of that -- passing height<=0 (e.g. from a test
// that only cares about content) is a no-op, matching padOrExtend's own
// contract, so this never truncates and never disturbs the existing
// content-focused callers.
func noCoreMessage(discoveryPath string, err error, theme render.Theme, width, height int) string {
	errText := "(no error recorded)"
	if err != nil {
		errText = err.Error()
	}
	safeErr := textsafe.Sanitize(errText)
	safePath := textsafe.Sanitize(discoveryPath)

	lines := []string{
		"No core running.",
		"",
		"pg-router tui cannot reach a core.",
		"",
		"Last error:",
		"  " + theme.Failing.Render(safeErr),
		"",
		"Looked for a discovery record at:",
		"  " + theme.Muted.Render(safePath),
		"",
		"To start one in the foreground:",
		"  pg-router run",
		"or supervise it as a long-running daemon (the pg-router-daemon service, if configured).",
		"",
		"Connects automatically when a core starts.",
		"",
		"Press q to quit.",
	}
	out := render.Block(strings.Join(lines, "\n"), render.EffectiveWidth(width))
	return padOrExtend(out, height)
}
