package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// TestPanes_DerivedHealthTwoAxes is this packet's own acceptance bar:
// derived health for listeners ranks disabled > excluded > cooling > ok;
// for sources disabled > excluded > failing > idle > N/A (unknown interval)
// > stale > ok (this task, pg2-mnf7t.1, widens sources to rank per-source
// staleness against ExpectedIntervalMs rather than the pool-wide tick); for
// the pool, the no-core/paused checks happen before
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
		minuteMs := time.Minute.Milliseconds()
		cases := []struct {
			name string
			s    Source
			want string
		}{
			{"disabled wins over excluded+failing", Source{Enabled: false, Excluded: true, Failure: failing}, "disabled"},
			{"excluded wins over failing", Source{Enabled: true, Excluded: true, Failure: failing}, "excluded"},
			{"failing wins over stale", Source{Enabled: true, Failure: failing, LastTick: now.Add(-1 * time.Hour), ExpectedIntervalMs: minuteMs}, "failing"},
			{"stale when ticked long ago", Source{Enabled: true, LastTick: now.Add(-1 * time.Hour), ExpectedIntervalMs: minuteMs}, "stale"},
			{"idle when never ticked", Source{Enabled: true}, "idle"},
			{"ok when ticked recently", Source{Enabled: true, LastTick: now, ExpectedIntervalMs: minuteMs}, "ok"},
		}
		for _, c := range cases {
			got := sourceHealthText(c.s, now, theme)
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: sourceHealthText = %q, want it to contain %q", c.name, got, c.want)
			}
		}
	})
}

// --- per-source staleness uses the source's own interval (this task, pg2-mnf7t.1) ---
//
// These four cases use render.Theme{} (the zero value) rather than
// render.NewTheme(...): Theme's own doc comment states "Zero-value renders
// plain text", so sourceHealthText's output compares equal to the exact
// expected string with no ANSI-stripping helper needed.

// A source with ExpectedIntervalMs == 0 (unknown) renders N/A, never stale,
// even though it last ticked long enough ago that a known interval would
// flag it stale.
func TestSourceHealthText_UnknownIntervalRendersNA(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Now().Add(-10 * time.Minute), ExpectedIntervalMs: 0}
	got := sourceHealthText(s, time.Now(), render.Theme{})
	if got != "N/A" {
		t.Fatalf("sourceHealthText() = %q, want %q", got, "N/A")
	}
}

// A source that has never ticked renders idle regardless of whether its
// interval is known -- idle outranks N/A, since "never started" and
// "cadence unknown" are different facts.
func TestSourceHealthText_NeverTickedIsIdleEvenWithKnownInterval(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Time{}, ExpectedIntervalMs: 30_000}
	got := sourceHealthText(s, time.Now(), render.Theme{})
	if got != "idle" {
		t.Fatalf("sourceHealthText() = %q, want %q (idle must outrank N/A)", got, "idle")
	}
}

// A source ticking every 5 minutes must read ok 2 minutes after its own
// last tick, even though a fast core tick would flag it stale under the
// OLD (pool-wide) threshold.
func TestSourceHealthText_UsesOwnIntervalNotCoreTick(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Now().Add(-2 * time.Minute), ExpectedIntervalMs: (5 * time.Minute).Milliseconds()}
	got := sourceHealthText(s, time.Now(), render.Theme{})
	if got != "ok" {
		t.Fatalf("sourceHealthText() = %q, want %q", got, "ok")
	}
}

// A source ticking every 5 minutes but silent for 20 minutes reads stale,
// judged against its OWN interval.
func TestSourceHealthText_StaleUsesOwnInterval(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Now().Add(-20 * time.Minute), ExpectedIntervalMs: (5 * time.Minute).Milliseconds()}
	got := sourceHealthText(s, time.Now(), render.Theme{})
	if !strings.HasPrefix(got, "stale") {
		t.Fatalf("sourceHealthText() = %q, want prefix %q", got, "stale")
	}
}

// TestRenderActivityOutcome_BudgetEscalationStylesDistinctlyFromOtherOutcomes
// is this bead's (pg2-fm2gw) acceptance criterion 3, at the unit level:
// outcomeBudgetEscalation renders wrapped in theme.Cooling (matching the
// same style listenerHealthText already uses for a listener's own
// transient backoff state -- see renderActivityOutcome's own doc for why),
// while every other outcome -- "delivered" included -- renders PLAIN, with
// no styling added, exactly as before this bead. Compared against
// theme.Cooling.Render(...) directly (never a raw ANSI string), the same
// convention render/theme_test.go and empty_state_test.go's
// TestDimIfPaused_NeverSuppressesConfigDerivedContent already use, because
// Render()'s literal escape-code output depends on the ambient
// color-profile detection of the test process.
func TestRenderActivityOutcome_BudgetEscalationStylesDistinctlyFromOtherOutcomes(t *testing.T) {
	theme := render.NewTheme(true) // color: styling actually applies

	got := renderActivityOutcome("budget_escalation", theme)
	want := theme.Cooling.Render("budget_escalation")
	if got != want {
		t.Errorf("renderActivityOutcome(budget_escalation) = %q, want the Cooling-wrapped text %q", got, want)
	}

	for _, other := range []string{"delivered", "missed", "declined", "dispatch_failed", "deduped"} {
		if got := renderActivityOutcome(other, theme); got != other {
			t.Errorf("renderActivityOutcome(%q) = %q, want it unstyled (%q) -- only budget_escalation gets a style", other, got, other)
		}
	}

	// The bead's own acceptance bar: distinct from the operator-pause
	// gate's own rendering (banner.go's renderPausedBanner), never merely
	// distinct from other Activity outcomes. theme.Cooling and theme.Paused
	// are deliberately different color tokens, so the two renders can never
	// coincide even before considering renderPausedBanner's own additional
	// Reverse(true)/full-width-banner treatment.
	pausedRendering := theme.Paused.Reverse(true).Render("PAUSED — dispatch halted · 0 in flight")
	if got == pausedRendering {
		t.Errorf("budget_escalation's rendering must never equal the operator-pause banner's rendering")
	}
}

// TestRenderActivityPane_BudgetEscalationEntryCarriesTheStyledOutcome is
// the pane-level acceptance bar: a real ActivityEntry carrying
// Outcome:"budget_escalation" renders through renderActivityPane with the
// SAME styled text renderActivityOutcome alone produces, and the plain
// event Type text survives verbatim alongside it (styling wraps, it does
// not replace or truncate -- matching TestDimIfPaused's own convention).
func TestRenderActivityPane_BudgetEscalationEntryCarriesTheStyledOutcome(t *testing.T) {
	theme := render.NewTheme(true)
	entries := []ActivityEntry{{Seq: 1, StartedAt: time.Now(), Type: "worker-ready", Outcome: "budget_escalation"}}

	got := renderActivityPane(entries, false, "(none)", theme)

	if !strings.Contains(got, "worker-ready") {
		t.Errorf("renderActivityPane lost the entry's Type; got:\n%s", got)
	}
	if !strings.Contains(got, theme.Cooling.Render("budget_escalation")) {
		t.Errorf("renderActivityPane did not render the styled budget_escalation outcome; got:\n%s", got)
	}
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

		for _, want := range []string{"Listeners", "Queues", "Sources", "Activity", "ROLE", "BINDS", "HEALTH", "DLVD", "DECL"} {
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
		for _, dropped := range []string{"Queues", "Sources"} {
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
//
// width=40 is deliberately too narrow to fit the long Role's natural
// width even after pg2-hlpuv's column-widening (paneColumnWidths widens
// the ROLE column from its 10-column static floor toward 40's available
// room, but nowhere near the Role's full 48 columns) -- this test keeps
// exercising the genuine "terminal lacks room" case pg2-8iy1m's guard
// covers. TestRenderListenersPane_WidensColumnsWhenRoomAllows (below) is
// pg2-hlpuv's own new acceptance bar for the opposite case.
func TestRenderListenersPane_OverflowingRoleKeepsBoxWellFormed(t *testing.T) {
	theme := render.NewTheme(false)
	listeners := []Listener{
		{Role: "short", Enabled: true, Delivered: 1, Declined: 2},
		{Role: "a-very-long-role-name-that-overflows-its-column", Enabled: true, Delivered: 3, Declined: 4},
	}

	got := renderListenersPane(listeners, render.TierTiny, 40, theme, "(none)", "Listeners", nil)
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
	if !strings.Contains(got, "…") {
		t.Errorf("terminal genuinely lacks room for the full Role -- it must still be ellipsis-truncated (pg2-8iy1m); got:\n%s", got)
	}
}

// TestRenderListenersPane_WidensColumnsWhenRoomAllows is pg2-hlpuv's own
// acceptance bar: a Role/name that would have been ellipsis-truncated
// under the tier's old fixed column widths must render IN FULL, with no
// "…", once the terminal has enough free space to show it -- and must
// fall back to truncating it (pg2-8iy1m's guard, unchanged) once the
// terminal genuinely does not, at every one of the three tiers.
func TestRenderListenersPane_WidensColumnsWhenRoomAllows(t *testing.T) {
	theme := render.NewTheme(false)
	const longRole = "a-very-long-role-name-that-overflows-its-column"
	listeners := []Listener{{Role: longRole, Enabled: true, Delivered: 3, Declined: 4}}

	for _, tier := range []int{render.TierTiny, render.TierNarrow, render.TierWide} {
		t.Run("ample width shows the full role", func(t *testing.T) {
			got := renderListenersPane(listeners, tier, 200, theme, "(none)", "Listeners", nil)
			if !strings.Contains(got, longRole) {
				t.Errorf("tier=%d width=200: expected the full role name un-truncated; got:\n%s", tier, got)
			}
			if strings.Contains(got, "…") {
				t.Errorf("tier=%d width=200: role should not be truncated when there is ample room; got:\n%s", tier, got)
			}
		})
		t.Run("narrow width still truncates (pg2-8iy1m preserved)", func(t *testing.T) {
			got := renderListenersPane(listeners, tier, 30, theme, "(none)", "Listeners", nil)
			if strings.Contains(got, longRole) {
				t.Errorf("tier=%d width=30: expected the role to be truncated, not shown in full; got:\n%s", tier, got)
			}
			if !strings.Contains(got, "…") {
				t.Errorf("tier=%d width=30: expected an ellipsis-truncated role; got:\n%s", tier, got)
			}
			// top border + header + 1 data row + bottom border, all the
			// same width -- no mid-row wrap from the truncated cell
			// [pg2-8iy1m].
			lines := strings.Split(got, "\n")
			if want := 4; len(lines) != want {
				t.Fatalf("tier=%d width=30: expected %d physical lines (no mid-row wrap), got %d; got:\n%s", tier, want, len(lines), got)
			}
			boxWidth := lipgloss.Width(lines[0])
			for i, l := range lines {
				if w := lipgloss.Width(l); w != boxWidth {
					t.Errorf("tier=%d width=30: line %d (%q) has width %d, want %d (every line of the box must align); got:\n%s", tier, i, l, w, boxWidth, got)
				}
			}
		})
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

// TestUnmatchedPartners_SingleRowVsAmbiguous is pg2-7ezqt's own acceptance
// bar for the partition itself: a type bound by exactly one listener row is
// that row's partner (perRow); a type bound by zero rows or by two-or-more
// rows has no single partner and stays in bannered instead.
func TestUnmatchedPartners_SingleRowVsAmbiguous(t *testing.T) {
	listeners := []Listener{
		{Role: "reviewer", Binds: []string{"pr.new", "shared.type"}},
		{Role: "triager", Binds: []string{"bead.new", "shared.type"}},
	}

	t.Run("zero matching rows stays bannered", func(t *testing.T) {
		bannered, perRow := unmatchedPartners([]string{"nobody.binds.this"}, listeners)
		if len(bannered) != 1 || bannered[0] != "nobody.binds.this" {
			t.Errorf("bannered = %v, want [\"nobody.binds.this\"]", bannered)
		}
		if len(perRow) != 0 {
			t.Errorf("perRow = %v, want empty (no single-row partner)", perRow)
		}
	})

	t.Run("exactly one matching row becomes that row's partner", func(t *testing.T) {
		bannered, perRow := unmatchedPartners([]string{"pr.new"}, listeners)
		if len(bannered) != 0 {
			t.Errorf("bannered = %v, want empty (moved inline)", bannered)
		}
		if got := perRow[0]; len(got) != 1 || got[0] != "pr.new" {
			t.Errorf("perRow[0] = %v, want [\"pr.new\"] (reviewer is the sole partner)", got)
		}
	})

	t.Run("two matching rows is ambiguous and stays bannered", func(t *testing.T) {
		bannered, perRow := unmatchedPartners([]string{"shared.type"}, listeners)
		if len(bannered) != 1 || bannered[0] != "shared.type" {
			t.Errorf("bannered = %v, want [\"shared.type\"] (no single row owns it)", bannered)
		}
		if len(perRow) != 0 {
			t.Errorf("perRow = %v, want empty", perRow)
		}
	})

	t.Run("a duplicate Binds entry within one row does not inflate its count past 1", func(t *testing.T) {
		dup := []Listener{{Role: "reviewer", Binds: []string{"pr.new", "pr.new"}}}
		bannered, perRow := unmatchedPartners([]string{"pr.new"}, dup)
		if len(bannered) != 0 {
			t.Errorf("bannered = %v, want empty (still a single row despite the duplicate bind)", bannered)
		}
		if got := perRow[0]; len(got) != 1 || got[0] != "pr.new" {
			t.Errorf("perRow[0] = %v, want [\"pr.new\"]", got)
		}
	})

	t.Run("no unmatched bindings returns nothing", func(t *testing.T) {
		bannered, perRow := unmatchedPartners(nil, listeners)
		if bannered != nil || perRow != nil {
			t.Errorf("unmatchedPartners(nil, ...) = (%v, %v), want (nil, nil)", bannered, perRow)
		}
	})
}

// TestRenderListenersPane_InlineUnmatchedMarker is pg2-7ezqt's own
// acceptance bar for the row-level rendering: a listener row whose bound
// type has exactly one row partner shows an inline marker naming that
// type, and a type with no single-row partner (mapped to zero or 2+ rows)
// leaves every row unmarked -- it is reported only via the banner
// (liveness.go's attentionLine), never rendered here.
func TestRenderListenersPane_InlineUnmatchedMarker(t *testing.T) {
	theme := render.NewTheme(false)

	t.Run("single-row partner renders the marker on its own row only", func(t *testing.T) {
		listeners := []Listener{
			{Role: "reviewer", Enabled: true, Binds: []string{"pr.new"}},
			{Role: "triager", Enabled: true, Binds: []string{"bead.new"}},
		}
		got := renderListenersPane(listeners, render.TierWide, 0, theme, "(none)", "Listeners", []string{"pr.new"})
		lines := strings.Split(got, "\n")

		var reviewerLine, triagerLine string
		for _, l := range lines {
			if strings.Contains(l, "reviewer") {
				reviewerLine = l
			}
			if strings.Contains(l, "triager") {
				triagerLine = l
			}
		}
		if !strings.Contains(reviewerLine, "not seen yet this run") || !strings.Contains(reviewerLine, "pr.new") {
			t.Errorf("reviewer row = %q, want it to carry the inline unmatched marker naming pr.new", reviewerLine)
		}
		if strings.Contains(triagerLine, "not seen yet this run") {
			t.Errorf("triager row = %q, want no marker (its own bind, bead.new, is not unmatched)", triagerLine)
		}
	})

	t.Run("a type with no single-row partner marks no row", func(t *testing.T) {
		listeners := []Listener{
			{Role: "reviewer", Enabled: true, Binds: []string{"pr.new"}},
			{Role: "triager", Enabled: true, Binds: []string{"pr.new"}},
		}
		got := renderListenersPane(listeners, render.TierWide, 0, theme, "(none)", "Listeners", []string{"pr.new"})
		if strings.Contains(got, "not seen yet this run") {
			t.Errorf("ambiguous (2-row) unmatched type must not render an inline marker anywhere; got:\n%s", got)
		}
	})

	t.Run("nil unmatchedBindings renders no marker", func(t *testing.T) {
		listeners := []Listener{{Role: "reviewer", Enabled: true, Binds: []string{"pr.new"}}}
		got := renderListenersPane(listeners, render.TierWide, 0, theme, "(none)", "Listeners", nil)
		if strings.Contains(got, "not seen yet this run") {
			t.Errorf("nil unmatchedBindings must render no marker; got:\n%s", got)
		}
	})
}

// TestListener_DeclinedBucketed_KnownReasons is Task 2's own red-first
// test: the two known DeclineReason strings ("busy"/"unavailable") bucket
// into their own named return values.
func TestListener_DeclinedBucketed_KnownReasons(t *testing.T) {
	l := Listener{Declined: 3, DeclinedByReason: map[string]int64{"busy": 2, "unavailable": 1}}
	busy, unavailable, other := l.DeclinedBucketed()
	if busy != 2 || unavailable != 1 || other != 0 {
		t.Fatalf("DeclinedBucketed() = (%d,%d,%d), want (2,1,0)", busy, unavailable, other)
	}
}

// TestListener_DeclinedBucketed_OverrideStringFoldsIntoOther proves an
// arbitrary DeclineDetail override string (e.g. "at-capacity") folds into
// other, and that the three-way sum still equals the flat Declined total.
func TestListener_DeclinedBucketed_OverrideStringFoldsIntoOther(t *testing.T) {
	l := Listener{Declined: 2, DeclinedByReason: map[string]int64{"busy": 1, "at-capacity": 1}}
	busy, unavailable, other := l.DeclinedBucketed()
	if busy != 1 || unavailable != 0 || other != 1 {
		t.Fatalf("DeclinedBucketed() = (%d,%d,%d), want (1,0,1)", busy, unavailable, other)
	}
	if busy+unavailable+other != l.Declined {
		t.Fatalf("bucketed sum %d != Declined %d", busy+unavailable+other, l.Declined)
	}
}

// TestListener_DeclinedBucketed_SumInvariantHoldsEvenOnCollidingOverrideText
// documents the one genuine ambiguity this bucketing has (spec's Review
// Focus): a DeclineDetail override that happens to equal "busy" verbatim is
// indistinguishable from a genuine DeclineBusy at this layer. The invariant
// this test actually guarantees is the sum, not which bucket it lands in.
func TestListener_DeclinedBucketed_SumInvariantHoldsEvenOnCollidingOverrideText(t *testing.T) {
	l := Listener{Declined: 5, DeclinedByReason: map[string]int64{"busy": 5}}
	busy, unavailable, other := l.DeclinedBucketed()
	if busy+unavailable+other != l.Declined {
		t.Fatalf("bucketed sum %d != Declined %d", busy+unavailable+other, l.Declined)
	}
}

// TestRenderListenersPane_NeverDispatchedRoleRendersCleanZeroState is Task
// 2's Review Focus: a declared role with zero delivered/declined, no
// self-report, must render clean placeholders, never a blank cell.
func TestRenderListenersPane_NeverDispatchedRoleRendersCleanZeroState(t *testing.T) {
	listeners := []Listener{{Role: "idle-role", Enabled: true}}
	out := renderListenersPane(listeners, render.TierWide, 0, render.Theme{}, "", "Listeners", nil)
	if !strings.Contains(out, "0 / 0 / 0") {
		t.Fatalf("expected a zero decline breakdown, got:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Fatalf("expected an em-dash for never-self-reported SELF, got:\n%s", out)
	}
	if !strings.Contains(out, "-") {
		t.Fatalf("expected a dash for never-delivered LAST DELIVERED, got:\n%s", out)
	}
}

// TestRenderListenersPane_WideTierIncludesFailColumn is this task's required
// RED test: the Wide tier must render a FAIL column carrying the per-role
// handler-failure count (this task's HandlerFailures field).
func TestRenderListenersPane_WideTierIncludesFailColumn(t *testing.T) {
	listeners := []Listener{{Role: "df-feedback", HandlerFailures: 3}}
	out := renderListenersPane(listeners, render.TierWide, 0, render.Theme{}, "", "Listeners", nil)
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "3") {
		t.Fatalf("rendered pane missing FAIL column/value:\n%s", out)
	}
}

// TestRenderListenersPane_NeverFailedRoleShowsCleanZero closes the Review
// Focus gap the earlier plan review found: a never-dispatched/never-failed
// role must render "0" in FAIL, not a blank cell.
func TestRenderListenersPane_NeverFailedRoleShowsCleanZero(t *testing.T) {
	listeners := []Listener{{Role: "idle-role", Enabled: true}}
	out := renderListenersPane(listeners, render.TierWide, 0, render.Theme{}, "", "Listeners", nil)
	if !strings.Contains(out, "0") {
		t.Fatalf("expected a clean 0 in FAIL for a never-failed role, got:\n%s", out)
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

// TestView_WidensPaneContentAcrossAllThreeTiers is pg2-hlpuv's own
// acceptance bar exercised through the REAL m.View() pipeline (rather than
// calling a pane renderer directly): a long Source name that overflows
// the Sources pane's static SOURCE column (12) must render in full, with
// no ellipsis, once the terminal is wide enough to fit the whole row --
// and must still fall back to an ellipsis-truncated name at a terminal
// that genuinely lacks the room, at each of the three render tiers
// (Tiny <80, Narrow 80-119, Wide >=120) [design: Task 4.6 Validation;
// pg2-hlpuv Acceptance Criteria].
//
// longName is 40 columns: Sources' own [SOURCE(12) LAST TICK(10)
// STATE(16)] static row plus the box's border/separator overhead totals
// 66 columns needed to show it whole. That comfortably fits at width=90
// (Narrow) and width=120 (Wide), but not at width=60 (Tiny) -- exercising
// both halves of the acceptance bar (shows in full where there's room;
// still truncates where there genuinely isn't) at real terminal widths,
// not just via an explicit tier constant.
func TestView_WidensPaneContentAcrossAllThreeTiers(t *testing.T) {
	const longName = "github-org-pull-requests-watcher-source"
	reply := StatusReply{
		Core:    CoreInfo{State: coreStateStarted},
		Sources: []Source{{Name: longName, Enabled: true, LastTick: time.Now()}},
	}

	cases := []struct {
		width        int
		tierName     string
		wantFullName bool
	}{
		{60, "Tiny", false},
		{90, "Narrow", true},
		{120, "Wide", true},
	}
	for _, c := range cases {
		t.Run(c.tierName, func(t *testing.T) {
			m := newTestModel(nil)
			m.width, m.height = c.width, 30
			m.screen = screenMain
			m.reply = reply
			got := m.View()

			// Isolate the Sources box's own lines -- the footer is
			// independently subject to the zone ladder's own global
			// width clip (zones.go's concatZones -> render.Block), which
			// can add its own unrelated ellipsis at a narrow terminal;
			// this test is only about the Sources pane's OWN column
			// widening, not the footer.
			var box []string
			inBox := false
			for _, l := range strings.Split(got, "\n") {
				if strings.Contains(l, "┌ Sources") {
					inBox = true
				}
				if inBox {
					box = append(box, l)
				}
				if inBox && strings.HasPrefix(strings.TrimSpace(l), "└") {
					break
				}
			}
			boxText := strings.Join(box, "\n")
			if boxText == "" {
				t.Fatalf("width=%d (%s): could not locate the Sources box in the rendered view; got:\n%s", c.width, c.tierName, got)
			}

			hasFullName := strings.Contains(boxText, longName)
			hasEllipsis := strings.Contains(boxText, "…")
			if c.wantFullName {
				if !hasFullName {
					t.Errorf("width=%d (%s): expected the full source name un-truncated; got Sources box:\n%s", c.width, c.tierName, boxText)
				}
				if hasEllipsis {
					t.Errorf("width=%d (%s): expected no truncation -- there is ample room; got Sources box:\n%s", c.width, c.tierName, boxText)
				}
			} else {
				if hasFullName {
					t.Errorf("width=%d (%s): expected the source name to be truncated, not shown in full; got Sources box:\n%s", c.width, c.tierName, boxText)
				}
				if !hasEllipsis {
					t.Errorf("width=%d (%s): expected an ellipsis-truncated source name (pg2-8iy1m preserved); got Sources box:\n%s", c.width, c.tierName, boxText)
				}
			}
		})
	}
}

// TestRenderQueuesPane_HeartbeatTypeHasNoBarButHasLabel is this packet's own
// acceptance bar: a queue type in the heartbeat set (pr.reconcile today)
// renders with a (heartbeat) label and no depth bar [design: Task 5, Step 1;
// Global Constraints].
func TestRenderQueuesPane_HeartbeatTypeHasNoBarButHasLabel(t *testing.T) {
	out := renderQueuesPane([]Queue{{Type: "pr.reconcile", Depth: 70}}, 0, "", "Queues")
	if strings.ContainsAny(out, "█░") {
		t.Fatalf("heartbeat queue row rendered a depth bar:\n%s", out)
	}
	if !strings.Contains(out, "(heartbeat)") {
		t.Fatalf("heartbeat queue row missing label:\n%s", out)
	}
}

// TestRenderQueuesPane_IncrementalTypeHasBarNoLabel is this packet's own
// acceptance bar: any other queue type renders a depthBar and no (heartbeat)
// label [design: Task 5, Step 1].
func TestRenderQueuesPane_IncrementalTypeHasBarNoLabel(t *testing.T) {
	out := renderQueuesPane([]Queue{{Type: "pr.changed", Depth: 3}}, 0, "", "Queues")
	if !strings.Contains(out, "█") {
		t.Fatalf("incremental queue row missing a depth bar:\n%s", out)
	}
	if strings.Contains(out, "(heartbeat)") {
		t.Fatalf("incremental queue row should not carry the heartbeat label:\n%s", out)
	}
}

// TestRenderQueuesPane_HeartbeatPrefixMatchNotExactMatch closes the round-1
// semantic post-check finding: the Global Constraint says the heartbeat set
// matches queue types that "start with" pr.reconcile, not only the exact
// string, so a dot-delimited prefixed variant must also render the
// heartbeat label.
func TestRenderQueuesPane_HeartbeatPrefixMatchNotExactMatch(t *testing.T) {
	out := renderQueuesPane([]Queue{{Type: "pr.reconcile.detail", Depth: 5}}, 0, "", "Queues")
	if strings.ContainsAny(out, "█░") {
		t.Fatalf("prefixed heartbeat queue row rendered a depth bar:\n%s", out)
	}
	if !strings.Contains(out, "(heartbeat)") {
		t.Fatalf("prefixed heartbeat queue row missing label:\n%s", out)
	}
}

// TestRenderQueuesPane_NonDelimitedFalsePositiveExcluded closes the round-2
// semantic post-check finding: a plain (non-dot-delimited) strings.HasPrefix
// check would also match "pr.reconciled" -- a plausible distinct future
// queue type name ("reconcile completed" event), not itself a heartbeat --
// so the match must require the dot boundary, not bare prefix.
func TestRenderQueuesPane_NonDelimitedFalsePositiveExcluded(t *testing.T) {
	out := renderQueuesPane([]Queue{{Type: "pr.reconciled", Depth: 4}}, 0, "", "Queues")
	if strings.Contains(out, "(heartbeat)") {
		t.Fatalf("\"pr.reconciled\" must not be misclassified as a heartbeat type via bare prefix match:\n%s", out)
	}
	if !strings.Contains(out, "█") {
		t.Fatalf("\"pr.reconciled\" should render an ordinary depth bar:\n%s", out)
	}
}

// TestRenderActivityPane_CapsToLastEightNewestFirst is this packet's own
// acceptance bar: renderActivityPane renders at most activityDisplayCap (8)
// entries, newest first -- the 4 oldest of 12 entries must be dropped
// [design: Task 6, Step 1; Global Constraints].
func TestRenderActivityPane_CapsToLastEightNewestFirst(t *testing.T) {
	now := time.Now()
	entries := make([]ActivityEntry, 12)
	for i := range entries {
		entries[i] = ActivityEntry{Seq: uint64(i), StartedAt: now.Add(time.Duration(i) * time.Second), Type: fmt.Sprintf("t%d", i)}
	}
	out := renderActivityPane(entries, false, "", render.Theme{})
	if strings.Contains(out, "t0") || strings.Contains(out, "t3") {
		t.Fatalf("expected the 4 oldest entries dropped, got:\n%s", out)
	}
	if !strings.Contains(out, "t11") {
		t.Fatalf("expected the newest entry present, got:\n%s", out)
	}
}

// TestRenderActivityPane_FewerThanCapDoesNotPanicOrPad is this packet's own
// acceptance bar: a ring holding fewer than 8 entries renders exactly what
// it has -- no panic, no padding with fake rows [design: Task 6, Step 1;
// Review Focus].
func TestRenderActivityPane_FewerThanCapDoesNotPanicOrPad(t *testing.T) {
	out := renderActivityPane([]ActivityEntry{{Seq: 1, StartedAt: time.Now(), Type: "x"}}, false, "", render.Theme{})
	// paneFrame (panes.go) emits exactly one line per content row plus a
	// top and bottom border line -- 1 activity entry means 3 total lines,
	// i.e. 2 newline separators. Padding toward activityDisplayCap would
	// add more; this asserts the exact count rather than "at least one
	// line," which would pass even if the implementation padded.
	if got := strings.Count(out, "\n"); got != 2 {
		t.Fatalf("expected exactly 2 newlines (1 row + top/bottom border, no padding) for a single entry, got %d:\n%s", got, out)
	}
}

// TestRenderActivityPane_RelativeTimestampNotAbsolute is this packet's own
// acceptance bar: every rendered entry's timestamp is relative (Xs ago/Xm
// ago), never an absolute HH:MM:SS string [design: Task 6, Step 1; Global
// Constraints].
func TestRenderActivityPane_RelativeTimestampNotAbsolute(t *testing.T) {
	out := renderActivityPane([]ActivityEntry{{Seq: 1, StartedAt: time.Now().Add(-5 * time.Second), Type: "x"}}, false, "", render.Theme{})
	if strings.Contains(out, ":") {
		t.Fatalf("expected a relative timestamp with no ':' (no HH:MM:SS), got:\n%s", out)
	}
	if !strings.Contains(out, "ago") {
		t.Fatalf("expected a relative 'ago' timestamp, got:\n%s", out)
	}
}
