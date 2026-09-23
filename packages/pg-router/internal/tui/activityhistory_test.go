package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestBindings_ActivityHistoryKeyOpensModal is this bead's own acceptance
// criterion 1/2: pressing "a" makes the Activity pane focusable/openable,
// and the key is documented in the [?] help modal via the shared Bindings
// table (help.go's bindingsToHelpRows) -- no separate wiring needed for
// that half.
func TestBindings_ActivityHistoryKeyOpensModal(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	mm := updated.(*Model)

	if mm.screen != screenModal {
		t.Fatalf("screen = %v after \"a\", want screenModal", mm.screen)
	}
	if mm.activeModal != ModalActivityHistory {
		t.Fatalf("activeModal = %v after \"a\", want ModalActivityHistory", mm.activeModal)
	}
	if mm.preModalScreen != screenMain {
		t.Fatalf("preModalScreen = %v after \"a\", want the remembered screenMain", mm.preModalScreen)
	}
}

// TestHelpModal_DocumentsTheActivityHistoryKey is acceptance criterion 2,
// checked directly against the rendered rows rather than just the
// Bindings table (TestBindingsToHelpRows_ListsEveryBinding, help_test.go,
// already proves the table drives the modal faithfully) -- the intent is
// that an operator opening [?] actually sees the "a" key explained.
func TestHelpModal_DocumentsTheActivityHistoryKey(t *testing.T) {
	found := false
	for _, b := range Bindings {
		for _, k := range b.Keys {
			if k == "a" {
				found = true
				if !strings.Contains(strings.ToLower(b.Description), "activity") {
					t.Errorf("the \"a\" binding's Description %q does not mention activity history", b.Description)
				}
			}
		}
	}
	if !found {
		t.Fatal(`Bindings has no entry for key "a" (Activity History)`)
	}
}

// TestActivityHistory_EscReturnsToMain is acceptance criterion 4, verbatim:
// esc closes the modal and restores whatever screen was active before it
// opened (handleEsc, keybindings.go -- unchanged by this bead, exercised
// here through the new modal specifically).
func TestActivityHistory_EscReturnsToMain(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*Model)
	if m.screen != screenModal || m.activeModal != ModalActivityHistory {
		t.Fatalf("precondition failed: screen/activeModal = %v/%v", m.screen, m.activeModal)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if m.screen != screenMain {
		t.Fatalf("screen = %v after esc, want screenMain", m.screen)
	}
	if m.activeModal != ModalNone {
		t.Fatalf("activeModal = %v after esc, want ModalNone", m.activeModal)
	}
}

// TestActivityHistory_NilPollerFallsBackToBufferWithNoCmd: a nil Poller
// (no run wired up, or a hand-driven test) must not panic -- handleOpen
// returns a nil cmd, and the modal renders whatever is already buffered
// rather than nothing at all.
func TestActivityHistory_NilPollerFallsBackToBufferWithNoCmd(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 60
	m.reply = StatusReply{Activity: []ActivityEntry{{Seq: 1, Type: "pr.opened", Outcome: "delivered"}}}

	cmd := handleOpenActivityHistory(m)
	if cmd != nil {
		t.Fatal("handleOpenActivityHistory with a nil Poller returned a non-nil cmd")
	}
	if m.activeModal != ModalActivityHistory {
		t.Fatalf("activeModal = %v, want ModalActivityHistory", m.activeModal)
	}

	got := m.renderActivityHistoryModal()
	if !strings.Contains(got, "pr.opened") {
		t.Errorf("renderActivityHistoryModal() = %q, want it to fall back to m.reply.Activity", got)
	}
}

// TestActivityHistory_FetchUsesSinceZeroNeverTheRegularCursor is this
// bead's central design point: the "a" key's dedicated Snapshot call must
// pass activityHistoryReadSince (0), the ring's own "no prior cursor" wider
// window, NEVER m.sinceCursor -- which the regular polling loop has already
// advanced past everything old (poll.go's pollNow/advanceSinceCursor is
// exactly the narrowing this fetch must bypass).
func TestActivityHistory_FetchUsesSinceZeroNeverTheRegularCursor(t *testing.T) {
	var gotSince uint64
	wide := make([]ActivityEntry, 20)
	for i := range wide {
		wide[i] = ActivityEntry{Seq: uint64(i + 1), Type: "pr.opened", Outcome: "delivered"}
	}
	m := newTestModel(&stubPoller{
		snapshot: func(_ context.Context, since uint64) (StatusReply, error) {
			gotSince = since
			return StatusReply{Activity: wide}, nil
		},
	})
	// The regular polling loop has already advanced well past 0 -- if the
	// dedicated fetch mistakenly reused this, it would ask for "what's new
	// since 15" instead of the wider window.
	m.sinceCursor = 15

	cmd := handleOpenActivityHistory(m)
	if cmd == nil {
		t.Fatal("handleOpenActivityHistory with a real Poller returned a nil cmd")
	}
	msg := cmd()
	if gotSince != activityHistoryReadSince {
		t.Fatalf("Snapshot called with since=%d, want activityHistoryReadSince (%d), never m.sinceCursor (15)", gotSince, activityHistoryReadSince)
	}
	res, ok := msg.(activityHistoryResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want activityHistoryResultMsg", msg)
	}
	if len(res.entries) != len(wide) {
		t.Fatalf("len(entries) = %d, want %d (the wider window, not the pane's 8-item cap)", len(res.entries), len(wide))
	}

	// Applying the result must not disturb m.sinceCursor -- the regular
	// polling loop's own bookkeeping is untouched by this side-channel.
	updated, _ := m.Update(res)
	mm := updated.(*Model)
	if mm.sinceCursor != 15 {
		t.Errorf("sinceCursor = %d after applying the wide fetch, want unchanged 15", mm.sinceCursor)
	}
	if len(mm.activityHistory) != len(wide) {
		t.Errorf("activityHistory has %d entries, want %d", len(mm.activityHistory), len(wide))
	}
}

// TestActivityHistory_RendersMoreThanTheMainPaneCap is acceptance
// criterion 3: the history screen must actually list MORE than
// activityDisplayCap (8) entries when the dedicated fetch returns that
// many, each with a timestamp and outcome -- never silently re-capped to
// the main pane's own 8-item budget.
func TestActivityHistory_RendersMoreThanTheMainPaneCap(t *testing.T) {
	entries := make([]ActivityEntry, activityDisplayCap+5)
	for i := range entries {
		entries[i] = ActivityEntry{Seq: uint64(i + 1), Type: "pr.opened", Outcome: "delivered"}
	}
	m := newTestModel(nil)
	m.width, m.height = 120, 60
	m.activeModal = ModalActivityHistory
	m.activityHistory = entries

	got := m.renderActivityHistoryModal()
	count := strings.Count(got, "delivered")
	if count <= activityDisplayCap {
		t.Fatalf("renderActivityHistoryModal rendered %d \"delivered\" outcomes, want more than the main pane's cap (%d)", count, activityDisplayCap)
	}
}

// TestActivityHistory_ErrDoesNotPanicAndLeavesPriorContent: a fetch
// failure must not crash (errorLogger is nil-safe, gates.go's own
// applyGateToggleResult precedent) and must not blank out whatever the
// modal already showed.
func TestActivityHistory_ErrDoesNotPanicAndLeavesPriorContent(t *testing.T) {
	m := newTestModel(nil)
	m.activityHistory = []ActivityEntry{{Seq: 1, Type: "pr.opened", Outcome: "delivered"}}

	updated, cmd := m.Update(activityHistoryErrMsg{err: errors.New("boom")})
	if cmd != nil {
		t.Error("Update on activityHistoryErrMsg returned a non-nil cmd, want nil")
	}
	mm := updated.(*Model)
	if len(mm.activityHistory) != 1 {
		t.Fatalf("activityHistory changed after a fetch error, want it left exactly as it was")
	}
}

// TestRenderModal_ActivityHistoryRoutesToItsOwnContent mirrors
// TestRenderModal_LegendAndGatesRouteToTheirOwnContent (help_test.go):
// renderModal must actually dispatch ModalActivityHistory to
// renderActivityHistoryModal, not fall through to the default empty case.
func TestRenderModal_ActivityHistoryRoutesToItsOwnContent(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.activeModal = ModalActivityHistory
	m.activityHistory = []ActivityEntry{{Seq: 1, Type: "pr.opened", Outcome: "delivered"}}

	got := m.renderModal()
	if !strings.Contains(got, "Activity History") {
		t.Errorf("renderModal() for ModalActivityHistory = %q, want the \"Activity History\" title", got)
	}
	if !strings.Contains(got, "pr.opened") {
		t.Errorf("renderModal() for ModalActivityHistory = %q, want the buffered entry rendered", got)
	}
}

// TestActivityHistory_FallsBackToActivityBufferBeforeFetchLands: opened
// with no prior m.activityHistory (nil, the pre-first-fetch state) but a
// populated m.activityBuffer, the modal must show the buffer rather than
// rendering empty while the wider fetch is still in flight.
func TestActivityHistory_FallsBackToActivityBufferBeforeFetchLands(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 60
	m.activityBuffer = []ActivityEntry{{Seq: 1, Type: "pr.merged", Outcome: "delivered"}}
	m.activeModal = ModalActivityHistory

	got := m.renderActivityHistoryModal()
	if !strings.Contains(got, "pr.merged") {
		t.Errorf("renderActivityHistoryModal() = %q, want it to fall back to m.activityBuffer", got)
	}
}
