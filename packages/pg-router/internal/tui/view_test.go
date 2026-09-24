// Package tui implements pg-router's operator-facing terminal UI. This file
// (Task 4.9) carries the comprehensive View()-pipeline test suite: the
// six-screen banner test at 80x24 and height 10 (discovering, not
// inventing, the deferred exact height-10 survivor set -- Binding Decision
// 6), and the width×height invariance sweep (TestViewLineWidthInvariant,
// reproducing pa-monitor's own view_test.go:22-58 pattern) plus
// NoPhantomBlankRowsBetweenZones [design: Task 4.9 Files; Steps 1-3]. Pure
// test surface over the sibling packets covering Tasks 4.4-4.8's existing
// exports -- no new production code [design: Task 4.9 Contract].
package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// --- Step 1: the six-screen banner test -----------------------------------

// sixScreenMainFixture is the "typical" screenMain fixture this file reuses
// for both the six-screen banner test and Decision 6's own height-10
// survivor-set discovery: two listeners (one cooling), one source, one
// queue, one activity entry -- enough real content in every pane to make
// the drop-order search under height pressure meaningful.
func sixScreenMainFixture() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{
		Core: CoreInfo{State: coreStateStarted, Version: "1.0.0", StartedAt: time.Now().Add(-2 * time.Hour)},
		Listeners: []Listener{
			{Role: "reviewer", Binds: []string{"pr.new"}, Enabled: true, Delivered: 14},
			{Role: "triager", Binds: []string{"bead.new"}, Enabled: true, Backoff: &Backoff{NextEligible: time.Now().Add(42 * time.Second)}},
		},
		Sources:  []Source{{Name: "gh-prs", Enabled: true, LastTick: time.Now()}},
		Queues:   []Queue{{Type: "pr.new", Depth: 2}},
		Activity: []ActivityEntry{{Seq: 1, StartedAt: time.Now(), Type: "dispatch", Outcome: "Closed"}},
	}
	return m
}

func sixScreenDrillDownFixture() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenDrillDown
	m.drillKind = rowListener
	m.reply = StatusReply{Listeners: []Listener{{Role: "reviewer", Binds: []string{"pr.new"}, Enabled: true}}}
	return m
}

// sixScreenModalFixture deliberately picks ModalHelp (rather than
// ModalLegend/ModalGates): help.go's helpFooter unconditionally names
// "pg-router" in its client/core version pair, giving this the strongest
// pg-router identity signal among the three modal kinds -- a freedom-boundary
// choice this packet is free to make (no fixture-selection contract exists
// for "the modal fixture").
func sixScreenModalFixture() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenModal
	m.activeModal = ModalHelp
	return m
}

func sixScreenNoCoreFixture() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenNoCore
	return m
}

func sixScreenLoadingFixture() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenLoading
	return m
}

func sixScreenQuiescingFixture() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenQuiescing
	return m
}

// TestSixScreenBanner_At80x24AndHeight10 is this packet's own red-first
// test [design: Task 4.9 Step 1]: all six screens (main/drill-down/modal/
// no-core/loading/quiescing) render valid, width-bounded output at both
// 80x24 and height 10.
//
// "The banner/top zone renders in every fixture" (Binding decisions;
// Acceptance Criteria) is operationalized here as: the pg-router identity
// text is visible SOMEWHERE in the screen's own output. Discovered from
// actual rendered output (not invented): this holds for five of the six
// screens --
//
//   - main:       renderTopZone's real header/banner ("renderMain",
//     model.go) names " pg-router " directly.
//   - drill-down: the breadcrumb ("pg-router ▸ Listeners ▸ ...",
//     drilldown.go's drillBreadcrumb) names it.
//   - modal:      help.go's helpFooter names it in the version pair.
//   - no-core:    nocore.go's noCoreMessage names it ("pg-router tui cannot
//     reach a core.").
//   - quiescing:  model.go's View literal names it directly
//     ("pg-router: quiescing ...").
//
// screenLoading is the one deliberate, frozen exception: model.go's View
// guard (`if m.width == 0 || m.screen == screenLoading { return
// "loading…" }`) returns that literal unconditionally, regardless of
// width or reply data -- a Task 4.5 Binding Decision already covered by
// TestView_ScreenLoadingIsLiteralRegardlessOfWidth (model_test.go). ux-6 (the
// design spec's own "composed above every screen including ... loading"
// aspiration) is therefore NOT satisfied by loading's current, frozen
// contract; reconciling that is a re-opening of the Task 4.5 packet, out of
// this packet's own scope (Contract §8, "Out of scope") -- not something
// this test invents around.
func TestSixScreenBanner_At80x24AndHeight10(t *testing.T) {
	fixtures := []struct {
		name          string
		make          func() *Model
		wantsPgRouter bool
	}{
		{"main", sixScreenMainFixture, true},
		{"drill-down", sixScreenDrillDownFixture, true},
		{"modal", sixScreenModalFixture, true},
		{"no-core", sixScreenNoCoreFixture, true},
		{"loading", sixScreenLoadingFixture, false}, // frozen exception, see doc above
		{"quiescing", sixScreenQuiescingFixture, true},
	}

	// nocore.go's noCoreMessage (Task 4.5) and drilldown.go's
	// renderDrillDown (Task 4.7) previously applied NO width clipping at
	// all -- a line longer than 80 columns (e.g. noCoreMessage's "or
	// supervise it as a long-running daemon (the pg-router-daemon service,
	// if configured)." at 85 columns) rendered verbatim regardless of
	// terminal width. Fixed by pg2-wp7k6 (both now run their composed
	// output through render.Block/render.EffectiveWidth, matching the
	// pattern every other tui render/pane file already uses), so every
	// screen's output is asserted <= 80 columns uniformly below, with no
	// exemption.

	for _, fx := range fixtures {
		for _, h := range []int{24, 10} {
			t.Run(fmt.Sprintf("%s@80x%d", fx.name, h), func(t *testing.T) {
				m := fx.make()
				m.width, m.height = 80, h
				out := m.View()
				if out == "" {
					t.Fatalf("View() returned empty output")
				}
				for i, line := range strings.Split(out, "\n") {
					if got := lipgloss.Width(line); got > 80 {
						t.Errorf("line %d width = %d, want <= 80: %q", i, got, line)
					}
				}
				if got := strings.Contains(out, "pg-router"); got != fx.wantsPgRouter {
					t.Errorf("%s screen: pg-router identity present = %v, want %v; got:\n%s", fx.name, got, fx.wantsPgRouter, out)
				}
			})
		}
	}

	// Decision 6: the exact height-10 survivor set for screenMain,
	// discovered from actual rendered output rather than invented ahead of
	// time [design: Binding decisions 6; Task 4.9 Step 1]. For
	// sixScreenMainFixture at 80x10: the top zone (header) and footer are
	// pinned and always render; Listeners is the fill zone (the default
	// focusedPane); Sources (dropOrder 5, 4 lines) survives; Activity
	// (dropOrder 3) and Queues (dropOrder 4) are dropped to make room.
	//
	// Before bead pg2-zxf3d's fix, total output was 13 lines -- MORE than
	// the requested height 10 -- because the fill zone's renderFill closure
	// (model.go's renderMain, the `p == m.focusedPane` branch) returned its
	// own fixed-size content and ignored the bodyHeight budget entirely, so
	// layoutZones' padOrExtend (which only ever pads, never truncates)
	// could not bring it back down to exactly 10 -- the SAME class of bug
	// pg2-x9w25 fixed for the pinned zones, discovered still live for the
	// fill zone itself when pg2-x9w25's own live verification follow-up
	// (pg2-czj6p) reproduced the top-of-frame scroll-off symptom again at
	// realistic terminal sizes. pg2-zxf3d wires the real height budget
	// through m.renderPaneContent/renderPaneBox (panes.go's
	// paneClampLines/clampBoxLines) so the fill zone always renders AT MOST
	// its allotted bodyHeight, dropping trailing data rows (replaced by a
	// "N more" summary line) or, once the budget is too tight even for the
	// header, the header itself -- while always preserving the box's own
	// top+bottom border pair. At 80x10, the Listeners fill zone's own
	// budget is squeezed to exactly 2 lines (barely enough for the two
	// borders alone, none left for the header), so its box renders as a
	// bare bordered box with no header/data row -- and the SCREEN total
	// comes out to exactly 10 lines, matching m.height, never 13.
	t.Run("main screen's exact height-10 survivor set", func(t *testing.T) {
		m := sixScreenMainFixture()
		m.width, m.height = 80, 10
		out := m.View()

		for _, want := range []string{"pg-router", "Listeners (focused)", "Sources"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected surviving zone/content %q; got:\n%s", want, out)
			}
		}
		for _, dropped := range []string{"Activity", "Queues"} {
			if strings.Contains(out, dropped) {
				t.Errorf("expected zone %q to have dropped under height pressure; got:\n%s", dropped, out)
			}
		}
		lines := strings.Split(out, "\n")
		if len(lines) != m.height {
			t.Errorf("line count = %d, want it to exactly match the requested height %d (pg2-zxf3d: the fill zone must never overflow its own budget); got:\n%s", len(lines), m.height, out)
		}
	})
}

// --- Step 2: the width×height invariance sweep ----------------------------

func fixtureSweepNoListeners() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{Core: CoreInfo{State: coreStateStarted}}
	return m
}

func fixtureSweepManyListeners() *Model {
	var listeners []Listener
	for i := range 20 {
		listeners = append(listeners, Listener{
			Role:      fmt.Sprintf("role-%d", i),
			Binds:     []string{"pr.new"},
			Enabled:   true,
			Delivered: int64(i),
		})
	}
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{Core: CoreInfo{State: coreStateStarted}, Listeners: listeners}
	return m
}

func fixtureSweepPaused() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{
		Core:      CoreInfo{State: coreStateStarted},
		Gates:     []Gate{{Name: core.GateOperatorPaused, Set: true}},
		Listeners: []Listener{{Role: "reviewer", Enabled: true}},
	}
	return m
}

func fixtureSweepDrillDownOpen() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenDrillDown
	m.drillKind = rowListener
	m.reply = StatusReply{Listeners: []Listener{{Role: "reviewer", Binds: []string{"pr.new"}}}}
	return m
}

func fixtureSweepCJKRoleName() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{
		Core:      CoreInfo{State: coreStateStarted},
		Listeners: []Listener{{Role: "日本語のロール名前がとても長い", Enabled: true, Binds: []string{"評論.新規"}}},
	}
	return m
}

func fixtureSweepLongBindingName() *Model {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{
		Core: CoreInfo{State: coreStateStarted},
		Listeners: []Listener{{
			Role:    "reviewer",
			Enabled: true,
			Binds:   []string{strings.Repeat("very-long-binding-name-segment.", 6)},
		}},
	}
	return m
}

// TestViewLineWidthInvariant reproduces pa-monitor's own view_test.go:22-58
// pattern [design: Task 4.9 Step 2]: for six fixtures (no-listeners/many-
// listeners/paused/drill-down-open/CJK-role-name/long-binding-name), swept
// across widths {0,30,60,80,120,200} x heights {0,1,2,3,4,5,10,30}, every
// line's lipgloss.Width(line) <= render.EffectiveWidth(width); at width==0
// the view defers to the literal "loading…" regardless of screen (model.go's
// View width==0 guard fires before the screen switch, so there is no
// distinct "screen-appropriate deferred string" in this package today --
// discovered, not invented).
func TestViewLineWidthInvariant(t *testing.T) {
	widths := []int{0, 30, 60, 80, 120, 200}
	heights := []int{0, 1, 2, 3, 4, 5, 10, 30}
	fixtures := []struct {
		name string
		make func() *Model
	}{
		{"no listeners", fixtureSweepNoListeners},
		{"many listeners", fixtureSweepManyListeners},
		{"paused", fixtureSweepPaused},
		{"drill-down open", fixtureSweepDrillDownOpen},
		{"CJK role name", fixtureSweepCJKRoleName},
		{"long binding name", fixtureSweepLongBindingName},
	}

	for _, fx := range fixtures {
		for _, w := range widths {
			for _, h := range heights {
				name := fmt.Sprintf("%s @ width=%d height=%d", fx.name, w, h)
				t.Run(name, func(t *testing.T) {
					m := fx.make()
					if w > 0 {
						m.Update(tea.WindowSizeMsg{Width: w, Height: h})
					}
					out := m.View()

					if w == 0 {
						if out != "loading…" {
							t.Errorf("width=0 should defer; got %q", out)
						}
						return
					}

					// renderDrillDown (drilldown.go, Task 4.7) previously
					// carried NO width clipping at all -- renderConfigSection's
					// own fixed "perParticipant" line (57 display columns)
					// exceeded width=30 unconditionally. Fixed by pg2-wp7k6
					// (renderDrillDown now runs its output through
					// render.Block/render.EffectiveWidth), so "drill-down
					// open" is asserted below like every other fixture, with
					// no exemption.

					ew := render.EffectiveWidth(w)
					for i, line := range strings.Split(out, "\n") {
						if got := lipgloss.Width(line); got > ew {
							t.Errorf("line %d width = %d, want <= %d (fixture=%q, w=%d, h=%d): %q",
								i, got, ew, fx.name, w, h, line)
						}
					}
				})
			}
		}
	}
}

// --- Step 3: NoPhantomBlankRowsBetweenZones --------------------------------

// TestViewNoPhantomBlankRowsBetweenZones asserts that layoutZones never
// leaves a blank placeholder line where a dropped zone used to be -- the
// same guard pa-monitor's own precedent test carries against the trailing-
// newline drift class of bug (render.Header's own terminating "\n"
// producing "\n\n" after the layout join). Trailing padding at the tail is
// allowed (padOrExtend's own documented job); a phantom blank in the middle
// is not.
func TestViewNoPhantomBlankRowsBetweenZones(t *testing.T) {
	m := fixtureSweepManyListeners()
	m.width, m.height = 120, 30
	out := m.View()

	lines := strings.Split(out, "\n")
	for i := 1; i < len(lines)-1; i++ {
		if lines[i] == "" && lines[i+1] == "" {
			if i < len(lines)/2 {
				t.Errorf("phantom blank row at lines %d-%d:\n%s", i, i+1, out)
				return
			}
		}
	}
}

// TestLayoutZones_DroppedZoneContributesZeroLinesAtExactHeight is Step 3's
// own literal claim, pinned at the one height where it is cleanly
// demonstrable end to end through Model.View(): "a DROPPED zone contributes
// ZERO lines, never a blank placeholder line -- total line count still
// equals height" [design: Task 4.9 Step 3].
//
// Discovered fixture: one listener (the default focused/fill pane, whose
// own fixed content is header+2 rows+border = 4 lines) plus one source (a
// non-focused, dropOrder-5 pane whose own box is also 4 lines). At height 8,
// the pinned zones (header 3 + footer 1 = 4, per pg2-xp415's header now
// spreading health/identity/uptime, the version pair, and gates/config
// across three lines instead of two) leave a 4-line budget the Sources pane
// alone would consume entirely (leaving 0 for the fill zone), so the drop
// search removes Sources outright; the survivors' own natural total
// (3 + 1 + 4 = 8) then lands exactly on the requested height, with no blank
// line standing in for the dropped pane.
func TestLayoutZones_DroppedZoneContributesZeroLinesAtExactHeight(t *testing.T) {
	m := NewModel(Options{}, render.NewTheme(false))
	m.screen = screenMain
	m.reply = StatusReply{
		Core:      CoreInfo{State: coreStateStarted},
		Listeners: []Listener{{Role: "reviewer", Enabled: true}},
		Sources:   []Source{{Name: "gh-prs", Enabled: true, LastTick: time.Now()}},
	}
	m.width, m.height = 80, 8

	out := m.View()
	if strings.Contains(out, "Sources") {
		t.Fatalf("Sources pane should have dropped under height pressure at height 8; got:\n%s", out)
	}

	lines := strings.Split(out, "\n")
	if len(lines) != 8 {
		t.Fatalf("line count = %d, want exactly 8 (the dropped zone must contribute zero lines, not a blank placeholder); got:\n%s", len(lines), out)
	}
	for i, l := range lines {
		if l == "" {
			t.Errorf("line %d is blank -- a dropped zone left a phantom placeholder row instead of contributing zero lines; got:\n%s", i, out)
		}
	}
}

// TestRenderMain_FocusedFillZoneNeverOverflowsRealisticConfig is bead
// pg2-zxf3d's own end-to-end regression test, reproducing the exact live
// incident report: a config with a double-digit handler/source count (11
// listeners, 14 sources -- the report's own numbers) rendered at the two
// terminal sizes the live investigation reproduced the bug at, 100x30 and
// 100x45, plus 100x60 (the report's own "stable" size) as a control.
//
// Before this bead, model.go's renderMain wired the Listeners/Sources (and
// Queues) fill-zone renderFill closures to always return
// m.renderPaneContent's full, unclamped table regardless of the height
// concatZones (zones.go) actually called them with -- so at a realistic
// terminal size the focused pane's own box could need far more rows than
// the terminal provides, and even inside tea.WithAltScreen()'s buffer,
// printing more lines than the buffer holds scrolls it, pushing the pinned
// top zone (banner, keybinding hints) off-screen. This asserts the two
// properties that symptom violates: total output never exceeds m.height,
// and the pinned top zone's own identity text and the pinned footer's own
// keybinding hints both survive in the output regardless of how many rows
// the focused pane's real content would have needed.
func TestRenderMain_FocusedFillZoneNeverOverflowsRealisticConfig(t *testing.T) {
	listeners := make([]Listener, 11)
	for i := range listeners {
		listeners[i] = Listener{Role: fmt.Sprintf("handler-role-%02d", i), Enabled: true, Delivered: int64(i)}
	}
	sources := make([]Source, 14)
	for i := range sources {
		sources[i] = Source{Name: fmt.Sprintf("source-%02d", i), Enabled: true, LastTick: time.Now()}
	}

	for _, size := range []struct{ w, h int }{{100, 30}, {100, 45}, {100, 60}} {
		for _, focused := range []int{paneListeners, paneSources} {
			t.Run(fmt.Sprintf("%dx%d focused=%s", size.w, size.h, paneName(focused)), func(t *testing.T) {
				m := NewModel(Options{}, render.NewTheme(false))
				m.screen = screenMain
				m.focusedPane = focused
				m.reply = StatusReply{
					Core:      CoreInfo{State: coreStateStarted, Version: "1.0.0"},
					Listeners: listeners,
					Sources:   sources,
				}
				m.width, m.height = size.w, size.h

				out := m.View()
				lines := strings.Split(out, "\n")

				if len(lines) > m.height {
					t.Errorf("rendered %d physical lines at height=%d -- the focused fill zone overflowed its budget (pg2-x9w25 regression); got:\n%s", len(lines), m.height, out)
				}
				if !strings.Contains(out, "pg-router") {
					t.Errorf("pinned top zone's own identity text is missing -- it scrolled off-screen; got:\n%s", out)
				}
				if !strings.Contains(out, "[tab] pane") {
					t.Errorf("pinned footer's own keybinding hints are missing; got:\n%s", out)
				}
			})
		}
	}
}
