package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// TestDrillDown_QueueAndActivityRowsAreNonFocusable is this packet's own
// red-first test [design: Task 4.7 Step 1]: pressing enter with the cursor
// on a queue row or an activity row must be a no-op -- no screen
// transition. Only rowListener/rowSource are members of focusableRowKind
// (comp-6); Queues is reachable via m.focusedPane and is asserted
// directly, while Activity is never reachable via m.focusedPane at all (it
// is rendered as its own zone, never one of the three tab-cycled panes) --
// the "only listeners/sources with actual rows can ever transition"
// sub-case below covers that structurally: with Activity the only
// populated field, m.focusedPane's zero value (paneListeners) has nothing
// to drill into, so enter is a no-op the same way.
func TestDrillDown_QueueAndActivityRowsAreNonFocusable(t *testing.T) {
	t.Run("queue row", func(t *testing.T) {
		m := newTestModel(nil)
		m.screen = screenMain
		m.focusedPane = paneQueues
		m.reply = StatusReply{Queues: []Queue{{Type: "issue", Depth: 3}}}

		cmd := m.enterDrillDown()

		if cmd != nil {
			t.Errorf("enterDrillDown() cmd = %v, want nil", cmd)
		}
		if m.screen != screenMain {
			t.Fatalf("screen = %v, want screenMain (enter on a queue row must be a no-op)", m.screen)
		}
	})

	t.Run("activity-only state", func(t *testing.T) {
		// Activity has no focus mechanism in the current Model at all: it
		// is never one of the three tab-cycled panes, so m.focusedPane can
		// never select it. With Listeners/Sources/Queues/Registry all
		// empty and only Activity populated, m.focusedPane's zero value
		// (paneListeners) has nothing to drill into -- enter is a no-op,
		// the same observable outcome a real "activity row" selection
		// would produce if one existed.
		m := newTestModel(nil)
		m.screen = screenMain
		m.reply = StatusReply{Activity: []ActivityEntry{{Seq: 1, Type: "produce"}}}

		m.enterDrillDown()

		if m.screen != screenMain {
			t.Fatalf("screen = %v, want screenMain (enter with only activity data present must be a no-op)", m.screen)
		}
	})

	t.Run("dispatched through Update via the real enter keybinding", func(t *testing.T) {
		m := newTestModel(nil)
		m.screen = screenMain
		m.focusedPane = paneQueues
		m.reply = StatusReply{Queues: []Queue{{Type: "issue", Depth: 1}}}

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		mm := updated.(*Model)

		if mm.screen != screenMain {
			t.Fatalf("screen = %v, want screenMain (enter dispatched through Update on a queue row must be a no-op)", mm.screen)
		}
	})
}

// TestDrillDown_SiblingStepping covers ux-12: [ / ] inside drill-down moves
// to the previous/next sibling row of the SAME kind, updating the
// breadcrumb text; stepping past either end is a no-op (clamped, not
// wrapping) [design: Task 4.7 Step 3].
func TestDrillDown_SiblingStepping(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenDrillDown
	m.reply = StatusReply{
		Listeners: []Listener{
			{Role: "alpha"},
			{Role: "beta"},
			{Role: "gamma"},
		},
		Sources: []Source{
			{Name: "src-one"},
			{Name: "src-two"},
		},
	}
	m.drillKind = rowListener
	m.drillIndex = 0

	if got := m.drillBreadcrumb(); !strings.Contains(got, "alpha") {
		t.Fatalf("breadcrumb = %q, want it to name alpha (index 0)", got)
	}

	m.stepSibling(1)
	if m.drillIndex != 1 {
		t.Fatalf("drillIndex after +1 = %d, want 1", m.drillIndex)
	}
	if got := m.drillBreadcrumb(); !strings.Contains(got, "beta") {
		t.Fatalf("breadcrumb = %q, want it to name beta (index 1)", got)
	}

	m.stepSibling(1)
	if m.drillIndex != 2 {
		t.Fatalf("drillIndex after second +1 = %d, want 2", m.drillIndex)
	}

	// Past the end: clamps at the last index, never wraps to 0.
	m.stepSibling(1)
	if m.drillIndex != 2 {
		t.Fatalf("drillIndex after stepping past the end = %d, want clamped at 2 (no wrap)", m.drillIndex)
	}
	if got := m.drillBreadcrumb(); !strings.Contains(got, "gamma") {
		t.Fatalf("breadcrumb = %q, want it to still name gamma", got)
	}

	// Back down, past the start: clamps at 0, never goes negative.
	m.stepSibling(-1)
	m.stepSibling(-1)
	m.stepSibling(-1)
	if m.drillIndex != 0 {
		t.Fatalf("drillIndex after stepping past the start = %d, want clamped at 0 (no wrap)", m.drillIndex)
	}

	t.Run("never crosses kinds", func(t *testing.T) {
		// Sources has 2 entries (max index 1), Listeners has 3 (max index
		// 2) -- if stepSibling used the wrong slice's length, this would
		// wrongly advance to 2. It must clamp at Sources' own length.
		m2 := newTestModel(nil)
		m2.screen = screenDrillDown
		m2.reply = m.reply
		m2.drillKind = rowSource
		m2.drillIndex = 1

		m2.stepSibling(1)

		if m2.drillIndex != 1 {
			t.Fatalf("drillIndex = %d, want clamped at 1 (Sources has only 2 entries; must not borrow Listeners' length)", m2.drillIndex)
		}
	})

	t.Run("no-op outside drill-down", func(t *testing.T) {
		m3 := newTestModel(nil)
		m3.screen = screenMain
		m3.reply = m.reply
		m3.drillKind = rowListener
		m3.drillIndex = 0

		m3.stepSibling(1)

		if m3.drillIndex != 0 {
			t.Fatalf("drillIndex = %d, want unchanged (stepSibling must no-op outside screenDrillDown)", m3.drillIndex)
		}
	})
}

// TestEnterDrillDown_TargetsFirstRowOfFocusedKind covers both directions
// of the Contract's Produces block: entering from paneListeners selects
// rowListener at index 0, entering from paneSources selects rowSource at
// index 0 (Task 4.6 delivers only pane-level focus, never a row-level
// cursor -- see Model.drillKind's own doc for why index 0 is this
// packet's freedom-boundary default).
func TestEnterDrillDown_TargetsFirstRowOfFocusedKind(t *testing.T) {
	t.Run("listeners", func(t *testing.T) {
		m := newTestModel(nil)
		m.screen = screenMain
		m.focusedPane = paneListeners
		m.reply = StatusReply{Listeners: []Listener{{Role: "reviewer"}, {Role: "triager"}}}

		m.enterDrillDown()

		if m.screen != screenDrillDown {
			t.Fatalf("screen = %v, want screenDrillDown", m.screen)
		}
		if m.drillKind != rowListener || m.drillIndex != 0 {
			t.Fatalf("drillKind/drillIndex = %v/%d, want rowListener/0", m.drillKind, m.drillIndex)
		}
	})

	t.Run("sources", func(t *testing.T) {
		m := newTestModel(nil)
		m.screen = screenMain
		m.focusedPane = paneSources
		m.reply = StatusReply{Sources: []Source{{Name: "src-one"}}}

		m.enterDrillDown()

		if m.screen != screenDrillDown {
			t.Fatalf("screen = %v, want screenDrillDown", m.screen)
		}
		if m.drillKind != rowSource || m.drillIndex != 0 {
			t.Fatalf("drillKind/drillIndex = %v/%d, want rowSource/0", m.drillKind, m.drillIndex)
		}
	})
}

// TestEnterDrillDown_EmptyPaneIsNoOp: a focused pane with nothing in it
// has no row to drill into, even though its kind is otherwise focusable.
func TestEnterDrillDown_EmptyPaneIsNoOp(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.focusedPane = paneListeners
	m.reply = StatusReply{}

	m.enterDrillDown()

	if m.screen != screenMain {
		t.Fatalf("screen = %v, want screenMain (empty Listeners pane must be a no-op)", m.screen)
	}
}

// TestEnterDrillDown_OnlyFiresFromScreenMain: the design's own screen
// transition table says drill-down is "Entered when: enter on a
// listener/source row" -- implicitly from screenMain, the only screen
// that ever renders a focusable pane. Pressed from any other screen it
// must be a no-op.
func TestEnterDrillDown_OnlyFiresFromScreenMain(t *testing.T) {
	for _, s := range []screen{screenNoCore, screenQuiescing, screenModal, screenLoading} {
		m := newTestModel(nil)
		m.screen = s
		m.focusedPane = paneListeners
		m.reply = StatusReply{Listeners: []Listener{{Role: "reviewer"}}}

		m.enterDrillDown()

		if m.screen != s {
			t.Errorf("starting from %v: screen = %v after enterDrillDown, want unchanged %v", s, m.screen, s)
		}
	}
}

// TestRenderDrillDown_ShowsSiblingSteppingHint covers pg2-az4xe: the "[" /
// "]" sibling-stepping keys (stepSibling above) work on screenDrillDown but
// had no on-screen affordance -- the operator only discovered them via the
// [?] help modal. renderDrillDown must now surface the hint directly on
// the drill-down screen itself, for both focusable row kinds.
func TestRenderDrillDown_ShowsSiblingSteppingHint(t *testing.T) {
	t.Run("listener", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 80, 24
		m.screen = screenDrillDown
		m.drillKind = rowListener
		m.reply = StatusReply{Listeners: []Listener{{Role: "reviewer"}}}

		got := m.renderDrillDown()

		if !strings.Contains(got, "[ / ]") {
			t.Fatalf("renderDrillDown() = %q, want it to surface a \"[ / ]\" sibling-stepping hint", got)
		}
	})

	t.Run("source", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 80, 24
		m.screen = screenDrillDown
		m.drillKind = rowSource
		m.reply = StatusReply{Sources: []Source{{Name: "src-one"}}}

		got := m.renderDrillDown()

		if !strings.Contains(got, "[ / ]") {
			t.Fatalf("renderDrillDown() = %q, want it to surface a \"[ / ]\" sibling-stepping hint", got)
		}
	})
}

// TestRenderConfigSection_LegacyFieldsPlusNote covers Task 4.7 Step 4's
// acceptance bar directly: the Config section renders the legacy-scalar
// fields (today always empty -- StatusReply does not decode
// `resolvedConfig` at all, Flagged for operator) plus a one-line note
// about perParticipant, and invents no per-kind content.
func TestRenderConfigSection_LegacyFieldsPlusNote(t *testing.T) {
	got := renderConfigSection(resolvedConfigView{})

	for _, want := range []string{"repoRoot", "beadsPrefix", "pollIntervalMs", "activeRoles", "activeQueries", "perParticipant"} {
		if !strings.Contains(got, want) {
			t.Errorf("Config section = %q, want it to name %q", got, want)
		}
	}
	if !strings.Contains(got, "perParticipant: {}") {
		t.Errorf("Config section = %q, want the one-line note to show perParticipant as empty ({})", got)
	}

	populated := renderConfigSection(resolvedConfigView{
		RepoRoot:       "/repo",
		BeadsPrefix:    "pg2-",
		PollIntervalMs: 5000,
		ActiveRoles:    3,
		ActiveQueries:  2,
	})
	for _, want := range []string{"/repo", "pg2-", "5000", "3", "2"} {
		if !strings.Contains(populated, want) {
			t.Errorf("populated Config section = %q, want it to render %q verbatim", populated, want)
		}
	}
}

// TestRenderSourceDetail_MatchesSourcesPaneParity covers pg2-v6ojj: the
// Source drill-down detail view (renderSourceDetail) previously showed only
// Name/Type/Mode/Health/LastTick -- omitting the NEXT CHECK IN countdown
// renderSourcesPane's own row already shows (sourceNextCheckText), and
// never rendering a source's Excluded flag or its Failure.NextEligible
// retry time as their own explicit values (both were only ever folded into
// sourceHealthText's short "excluded"/"failing xN" string). This test pins
// all three gaps closed.
func TestRenderSourceDetail_MatchesSourcesPaneParity(t *testing.T) {
	theme := render.NewTheme(false) // mono: plain text tokens, no ANSI noise

	t.Run("healthy source shows the NEXT CHECK IN countdown", func(t *testing.T) {
		now := time.Now()
		s := Source{
			Name:               "gh-prs",
			Type:               "github",
			Mode:               "poll",
			Enabled:            true,
			Excluded:           false,
			LastTick:           now,
			ExpectedIntervalMs: 5 * time.Minute.Milliseconds(),
		}

		got := renderSourceDetail(s, now, theme)

		want := sourceNextCheckText(s, now)
		if !strings.Contains(got, "NEXT CHECK IN:") || !strings.Contains(got, want) {
			t.Errorf("renderSourceDetail() = %q, want it to contain \"NEXT CHECK IN:\" and the countdown %q (matching renderSourcesPane's own row)", got, want)
		}
		// A healthy source has no Failure, so no "next eligible:" line should
		// appear at all.
		if strings.Contains(got, "next eligible:") {
			t.Errorf("renderSourceDetail() = %q, want no \"next eligible:\" line for a healthy source with Failure == nil", got)
		}
	})

	t.Run("failing source shows Failure.NextEligible explicitly, not just folded into the health string", func(t *testing.T) {
		now := time.Now()
		nextEligible := now.Add(2 * time.Minute)
		s := Source{
			Name:     "gh-issues",
			Type:     "github",
			Mode:     "poll",
			Enabled:  true,
			LastTick: now,
			Failure:  &Failure{Count: 3, NextEligible: nextEligible},
		}

		got := renderSourceDetail(s, now, theme)

		if !strings.Contains(got, "failing") {
			t.Errorf("renderSourceDetail() = %q, want the Health line to still show \"failing\" (unchanged existing behavior)", got)
		}
		wantTime := nextEligible.Format("15:04:05")
		if !strings.Contains(got, "next eligible:") || !strings.Contains(got, wantTime) {
			t.Errorf("renderSourceDetail() = %q, want an explicit \"next eligible: %s\" line naming Failure.NextEligible", got, wantTime)
		}
	})

	t.Run("excluded source shows Excluded explicitly", func(t *testing.T) {
		now := time.Now()
		s := Source{
			Name:     "gh-prs",
			Type:     "github",
			Mode:     "poll",
			Enabled:  true,
			Excluded: true,
		}

		got := renderSourceDetail(s, now, theme)

		if !strings.Contains(got, "Excluded:") || !strings.Contains(got, "true") {
			t.Errorf("renderSourceDetail() = %q, want an explicit \"Excluded: true\" line (not just folded into the \"excluded\" health string)", got)
		}
	})

	t.Run("non-excluded source's Excluded line reads false", func(t *testing.T) {
		now := time.Now()
		s := Source{Name: "gh-prs", Type: "github", Mode: "poll", Enabled: true, Excluded: false}

		got := renderSourceDetail(s, now, theme)

		if !strings.Contains(got, "Excluded:") || !strings.Contains(got, "false") {
			t.Errorf("renderSourceDetail() = %q, want an explicit \"Excluded: false\" line", got)
		}
	})
}
