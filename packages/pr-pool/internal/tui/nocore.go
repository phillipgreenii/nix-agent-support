package tui

import (
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
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
// daemon (the pr-pool-daemon service, if configured).", 85 columns)
// otherwise exceeds narrow widths unconditionally.
func noCoreMessage(discoveryPath string, err error, theme render.Theme, width int) string {
	errText := "(no error recorded)"
	if err != nil {
		errText = err.Error()
	}
	safeErr := textsafe.Sanitize(errText)
	safePath := textsafe.Sanitize(discoveryPath)

	lines := []string{
		"No core running.",
		"",
		"pr-pool tui cannot reach a core.",
		"",
		"Last error:",
		"  " + theme.Failing.Render(safeErr),
		"",
		"Looked for a discovery record at:",
		"  " + theme.Muted.Render(safePath),
		"",
		"To start one in the foreground:",
		"  pr-pool run",
		"or supervise it as a long-running daemon (the pr-pool-daemon service, if configured).",
		"",
		"Connects automatically when a core starts.",
		"",
		"Press q to quit.",
	}
	return render.Block(strings.Join(lines, "\n"), render.EffectiveWidth(width))
}
