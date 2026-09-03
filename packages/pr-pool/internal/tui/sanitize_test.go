// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.9) carries the sanitize-composes-with-width test: a payload string
// that would truncate mid-escape-sequence if width were measured against
// the raw string instead of the one textsafe.Sanitize returns, proving the
// sanitize-BEFORE-measure ordering end to end through the View() pipeline
// [design: Task 4.9 Files (sanitize_test.go); Step 4].
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestSanitizeComposesWithWidth [design: Task 4.9 Step 4]. dirty carries, in
// this order: a combining-mark sequence ("e" + U+0301 COMBINING ACUTE
// ACCENT -- NOT the precomposed "é": textsafe.Sanitize's own doc says
// ordinary UTF-8 text, including combining marks, passes through
// byte-for-byte untouched), an OSC-8 hyperlink escape (opened, then closed,
// both BEL-terminated), a bare C0 control byte (0x01, SOH), and enough
// plain text to reach past the Listeners pane's fixed 10-column ROLE
// width (formatPaneRow's lipgloss.NewStyle().Width(10), panes.go).
//
// renderListenersPane (panes.go) calls textsafe.Sanitize(l.Role) BEFORE
// building the row, so by the time the ROLE cell is width-clipped, the raw
// control/escape bytes are already gone -- this test proves that ordering
// empirically, through the real View() pipeline, rather than by inspecting
// the call order in panes.go.
func TestSanitizeComposesWithWidth(t *testing.T) {
	dirty := "é" + "\x1b]8;;http://example.com\x07" + "click" + "\x1b]8;;\x07" + "\x01" + "reviewerlongtail"

	// Guard the fixture's own premise: naively slicing the RAW string to
	// the same byte budget a non-escape-aware width computation would use
	// cuts mid-escape-sequence, leaving a dangling, unterminated OSC-8
	// sequence -- exactly the corruption textsafe.Sanitize's own package
	// doc warns "measuring the raw string first... silently corrupts the
	// width budget." If this ever stops holding, the payload/budget below
	// needs adjusting for the test to still prove anything.
	if naive := dirty[:10]; !strings.ContainsRune(naive, 0x1b) {
		t.Fatalf("test fixture assumption broken: dirty[:10] = %q no longer straddles the OSC-8 escape -- adjust the payload", naive)
	}

	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{
		Core:      CoreInfo{State: coreStateStarted},
		Listeners: []Listener{{Role: dirty, Enabled: true, Binds: []string{"pr.new"}}},
	}
	m.width, m.height = 80, 24

	out := m.View()

	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("View() output still carries a raw ESC byte -- sanitize did not run before width measurement:\n%s", out)
	}
	if strings.ContainsRune(out, 0x01) {
		t.Fatalf("View() output still carries the raw C0 control byte (0x01):\n%s", out)
	}
	if strings.Contains(out, "http://example.com") {
		t.Fatalf("View() output still carries the raw OSC-8 hyperlink target -- sanitize did not strip it:\n%s", out)
	}
	// The sanitized text is "éclickreviewerlongtail"; at the ROLE column's
	// fixed 10-column width it truncates to "éclickrevi" (10 display
	// cells -- the combining sequence renders as one cell). "click" is the
	// payload's own printable text (not one of the dirty bytes), so its
	// survival confirms sanitize stripped the ESCAPE SEQUENCES specifically
	// rather than truncating the whole cell down to nothing.
	if !strings.Contains(out, "click") {
		t.Fatalf("View() output dropped the payload's own printable text, not just the dirty bytes:\n%s", out)
	}

	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > render.EffectiveWidth(80) {
			t.Errorf("line %d width = %d, want <= %d (sanitize must compose with the width bound, not just run independently of it): %q",
				i, got, render.EffectiveWidth(80), line)
		}
	}

	// Direct confirmation that textsafe.Sanitize itself (Task 4.9's own
	// Expected additional reads: textsafe.go's Sanitize) is the function
	// responsible for the stripping observed above.
	clean := textsafe.Sanitize(dirty)
	if strings.ContainsAny(clean, "\x1b\x01") {
		t.Fatalf("textsafe.Sanitize left raw control bytes in %q", clean)
	}
}
