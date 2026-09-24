package tui

import (
	"os"
	"strings"
	"testing"
)

// TestRunUsesAltScreen is bead pg2-x9w25's regression guard: Run's
// tea.NewProgram call MUST construct the program with tea.WithAltScreen(),
// matching pa-monitor's own precedent (packages/pa-monitor/cmd/pa-monitor/
// tui_remote.go's tea.NewProgram(model, tea.WithAltScreen())).
//
// Without the alternate-screen buffer, bubbletea renders inline at
// whatever row the cursor happened to be on, rather than a fixed
// full-screen region starting at row 0. If the initial frame is taller
// than the terminal's remaining visible rows below the cursor, the
// terminal scrolls and the TOP of the frame -- the pinned header/PAUSED-
// banner zone, including the P/R keybinding hints -- is what scrolls off,
// even when the layout math (zones.go) sized the frame correctly for
// m.height. This is a pure missing-flag defect, not something the layout
// math can compensate for.
//
// It is guarded here as a source-text check rather than a rendering
// assertion because tea.ProgramOption values carry no exported/comparable
// state (tea.WithAltScreen returns an unexported closure over an
// unexported field) -- there is no way to introspect an already-
// constructed tea.Program and ask "was this option applied." The only
// observable evidence this option was ever passed is the source line
// itself, which is exactly the artifact a later refactor could silently
// drop (as happened here) with nothing else -- build, vet, or any
// rendering test -- catching it.
func TestRunUsesAltScreen(t *testing.T) {
	const file = "model.go"
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	text := string(src)

	const marker = "tea.NewProgram("
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("%s: no %q call found -- Run's tea.NewProgram construction site may have moved; update this guard's search", file, marker)
	}

	// Find the matching close paren by depth-counting rather than a plain
	// scan to the first ")": the call's own arguments are themselves calls
	// (tea.WithOutput(out), tea.WithAltScreen()), each contributing a
	// nested "(" / ")" pair that a naive scan would stop on early.
	openAt := idx + len(marker) - 1 // index of the call's own "("
	depth := 0
	end := -1
	for i := openAt; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("%s: could not find the matching close paren for the %q call starting at byte %d", file, marker, idx)
	}

	call := text[idx:end]
	if !strings.Contains(call, "tea.WithAltScreen()") {
		t.Fatalf(
			"%s's tea.NewProgram call %q does not include tea.WithAltScreen() -- "+
				"without it the TUI renders inline instead of in a fixed full-screen "+
				"buffer, letting the top of the frame (PAUSED banner, pinned zones, "+
				"P/R keybinding hints) scroll off under normal terminal use (bead pg2-x9w25)",
			file, call,
		)
	}
}
