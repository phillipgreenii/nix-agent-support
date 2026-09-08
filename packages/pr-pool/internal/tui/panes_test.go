package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestPanes_DerivedHealthTwoAxes is this packet's own acceptance bar:
// derived health for listeners ranks disabled > excluded > cooling > ok;
// for sources disabled > excluded > failing > stale > idle > ok; for the
// pool, the no-core/paused checks happen before
// core-tick-wedged/degraded/ok [design: Task 4.6 Binding decisions].
func TestPanes_DerivedHealthTwoAxes(t *testing.T) {
	theme := render.NewTheme(false) // mono: plain text tokens, no ANSI noise

	t.Run("pool", func(t *testing.T) {
		cases := []struct {
			name                          string
			hasCore, gated, wedged, degra bool
			want                          string
		}{
			{"no-core wins over everything", false, true, true, true, "no-core"},
			{"paused wins over wedged/degraded", true, true, true, true, "paused"},
			{"tick-wedged wins over degraded", true, false, true, true, "core-tick-wedged"},
			{"degraded when nothing higher applies", true, false, false, true, "degraded"},
			{"ok when nothing applies", true, false, false, false, "ok"},
		}
		for _, c := range cases {
			if got := poolHealthText(c.hasCore, c.gated, c.wedged, c.degra, theme); got != c.want {
				t.Errorf("%s: poolHealthText = %q, want %q", c.name, got, c.want)
			}
		}
	})

	t.Run("listener", func(t *testing.T) {
		cooling := &Backoff{NextEligible: time.Now().Add(42 * time.Second)}
		cases := []struct {
			name string
			l    Listener
			want string
		}{
			{"disabled wins over excluded+cooling", Listener{Enabled: false, Excluded: true, Backoff: cooling}, "disabled"},
			{"excluded wins over cooling", Listener{Enabled: true, Excluded: true, Backoff: cooling}, "excluded"},
			{"cooling when enabled+included", Listener{Enabled: true, Backoff: cooling}, "cooling"},
			{"ok otherwise", Listener{Enabled: true}, "ok"},
		}
		for _, c := range cases {
			got := listenerHealthText(c.l, theme)
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: listenerHealthText = %q, want it to contain %q", c.name, got, c.want)
			}
		}
	})

	t.Run("source", func(t *testing.T) {
		now := time.Now()
		failing := &Failure{Count: 2}
		cases := []struct {
			name string
			s    Source
			want string
		}{
			{"disabled wins over excluded+failing", Source{Enabled: false, Excluded: true, Failure: failing}, "disabled"},
			{"excluded wins over failing", Source{Enabled: true, Excluded: true, Failure: failing}, "excluded"},
			{"failing wins over stale", Source{Enabled: true, Failure: failing, LastTick: now.Add(-1 * time.Hour)}, "failing"},
			{"stale when ticked long ago", Source{Enabled: true, LastTick: now.Add(-1 * time.Hour)}, "stale"},
			{"idle when never ticked", Source{Enabled: true}, "idle"},
			{"ok when ticked recently", Source{Enabled: true, LastTick: now}, "ok"},
		}
		for _, c := range cases {
			got := sourceHealthText(c.s, 1000, now, theme)
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: sourceHealthText = %q, want it to contain %q", c.name, got, c.want)
			}
		}
	})
}

// TestPanes_ThreeTierMockups is this packet's own acceptance bar: Wide/
// Narrow/Tiny tier renders show the correct column SET and pane SET per
// the design's own mockups (§4.3) -- column/pane SET comparisons, never
// golden-string matches (those are reserved for header/banner strings
// only) [design: Task 4.6 Validation].
func TestPanes_ThreeTierMockups(t *testing.T) {
	reply := StatusReply{
		Core:      CoreInfo{State: "started"},
		Listeners: []Listener{{Role: "reviewer", Binds: []string{"pr.new"}, Enabled: true, Delivered: 14}},
		Sources:   []Source{{Name: "gh-prs", Enabled: true, LastTick: time.Now()}},
		Queues:    []Queue{{Type: "pr.new", Depth: 2}},
		Registry:  []Registration{{ID: "h1", Kind: "handler", State: "started", Self: "healthy"}},
		Activity:  []ActivityEntry{{Seq: 1, StartedAt: time.Now(), Type: "dispatch", Outcome: "Closed"}},
	}

	t.Run("Wide (>=120 cols) shows every pane and the BINDS column", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 120, 30
		m.screen = screenMain
		m.reply = reply
		got := m.View()

		for _, want := range []string{"Listeners", "Queues", "Sources", "Registry", "Activity", "ROLE", "BINDS", "HEALTH", "DLVD", "DECL"} {
			if !strings.Contains(got, want) {
				t.Errorf("Wide: missing %q; got:\n%s", want, got)
			}
		}
	})

	t.Run("Narrow (80-119 cols) drops the BINDS column but keeps DECL", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 90, 30
		m.screen = screenMain
		m.reply = reply
		got := m.View()

		if strings.Contains(got, "BINDS") {
			t.Errorf("Narrow: BINDS column should be dropped; got:\n%s", got)
		}
		for _, want := range []string{"ROLE", "HEALTH", "DLVD", "DECL"} {
			if !strings.Contains(got, want) {
				t.Errorf("Narrow: missing %q; got:\n%s", want, got)
			}
		}
	})

	t.Run("Tiny (<80 cols) under height pressure shows only the focused pane, without BINDS or DECL", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 60, 6 // short terminal: forces the drop-order search
		m.screen = screenMain
		m.reply = reply
		// m.focusedPane defaults to paneListeners.
		got := m.View()

		if !strings.Contains(got, "Listeners") {
			t.Fatalf("Tiny: focused pane (Listeners) must survive; got:\n%s", got)
		}
		if strings.Contains(got, "BINDS") {
			t.Errorf("Tiny: BINDS column should be dropped; got:\n%s", got)
		}
		if strings.Contains(got, "DECL") {
			t.Errorf("Tiny: DECL column should be dropped; got:\n%s", got)
		}
		for _, dropped := range []string{"Queues", "Sources", "Registry"} {
			if strings.Contains(got, dropped) {
				t.Errorf("Tiny: unfocused pane %q should have dropped under height pressure; got:\n%s", dropped, got)
			}
		}
	})
}

// TestFormatPaneRow_OverflowingCellTruncatesInsteadOfWrapping guards
// against lipgloss's Style.Width() word-wrapping a cell wider than its
// column instead of only padding it -- the same fixed-width-column shape
// already fixed for render.Modal's Left column (pg2-y6sy5) and legendRows'
// description column (pg2-58ecs), found recurring in panes.go's own row
// layout (pg2-8iy1m). Before the fix, an overflowing first cell split into
// multiple physical lines, so the row's HEALTH/DLVD cells landed on their
// own line with no leading column gap at all instead of one space after
// the (truncated) first cell.
func TestFormatPaneRow_OverflowingCellTruncatesInsteadOfWrapping(t *testing.T) {
	headers := []string{"ROLE", "HEALTH", "DLVD"}
	widths := []int{10, 14, 6}

	got := formatPaneRow([]string{"a-very-long-role-name-that-overflows", "ok", "3"}, widths)

	if strings.Contains(got, "\n") {
		t.Fatalf("formatPaneRow must render exactly one physical line; got:\n%q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("overflowing cell should be truncated with an ellipsis; got %q", got)
	}
	if !strings.Contains(got, "ok") || !strings.Contains(got, "3") {
		t.Errorf("HEALTH/DLVD cells must still appear on the same line; got %q", got)
	}

	// The header row (whose cells all fit) and the overflowing data row
	// must render at the exact same total width -- that is what "columns
	// align within a pane" means for a fixed-width table.
	header := formatPaneRow(headers, widths)
	if hw, gw := lipgloss.Width(header), lipgloss.Width(got); hw != gw {
		t.Errorf("overflowing row width %d must match header row width %d (columns misaligned)", gw, hw)
	}
}

// TestRenderListenersPane_OverflowingRoleKeepsBoxWellFormed is the
// higher-level acceptance bar for the same fix: rendered through the real
// pane box (paneFrame), every physical line -- top border, header, every
// data row, bottom border -- must be the same visual width, and a
// pathologically long Role must not blow up the number of physical lines
// the box occupies (one per listener, not one-plus-per-listener from a
// mid-cell word-wrap) [pg2-8iy1m].
func TestRenderListenersPane_OverflowingRoleKeepsBoxWellFormed(t *testing.T) {
	theme := render.NewTheme(false)
	listeners := []Listener{
		{Role: "short", Enabled: true, Delivered: 1, Declined: 2},
		{Role: "a-very-long-role-name-that-overflows-its-column", Enabled: true, Delivered: 3, Declined: 4},
	}

	got := renderListenersPane(listeners, render.TierTiny, theme, "(none)", "Listeners")
	lines := strings.Split(got, "\n")

	// top border + header + 2 data rows + bottom border.
	if want := 5; len(lines) != want {
		t.Fatalf("expected %d physical lines (no mid-row wrap), got %d; got:\n%s", want, len(lines), got)
	}
	width := lipgloss.Width(lines[0])
	for i, l := range lines {
		if w := lipgloss.Width(l); w != width {
			t.Errorf("line %d (%q) has width %d, want %d (every line of the box must align); got:\n%s", i, l, w, width, got)
		}
	}
}

// TestPaneFrame_TopBorderMatchesContentWidth guards paneFrame's own border
// math directly, with no overflow involved: the top border line ("┌ Title
// ─...─┐") must render at the exact same visual width as every content
// line and the bottom border ("│ ... │" / "└─...─┘") [pg2-8iy1m]. Before
// the fix the dash-count formula was one column short, so the top border
// was narrower than the rest of the box on every render -- the pane's own
// frame didn't align with itself, independent of any cell overflow.
func TestPaneFrame_TopBorderMatchesContentWidth(t *testing.T) {
	got := paneFrame("Queues", []string{"TYPE   DEPTH", "pr.new 2"})
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 physical lines (top, 2 content, bottom), got %d; got:\n%s", len(lines), got)
	}
	want := lipgloss.Width(lines[1]) // a content line's width is the reference.
	for i, l := range lines {
		if w := lipgloss.Width(l); w != want {
			t.Errorf("line %d (%q) has width %d, want %d matching the box's content width; got:\n%s", i, l, w, want, got)
		}
	}
}

// TestRenderRegistryPane_OmittedEntirelyWhenEmpty pins v1's own carried
// decision (§3, restated at §4.3 for Narrow): the Registry pane is
// omitted entirely -- not shown as an empty box -- when the registry has
// no entries, unless it is the focused pane.
func TestRenderRegistryPane_OmittedEntirelyWhenEmpty(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 30
	m.screen = screenMain
	m.reply = StatusReply{Core: CoreInfo{State: "started"}}

	got := m.View()
	if strings.Contains(got, "Registry") {
		t.Errorf("empty Registry should be omitted entirely; got:\n%s", got)
	}
}
