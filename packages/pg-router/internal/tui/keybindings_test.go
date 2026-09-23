package tui

import (
	"sort"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// specKeys are every key the design's key table names -- this packet's
// own Files entry for keybindings.go: "covering every key in [the]
// table" [design: Task 4.8 Files].
var specKeys = []string{"P", "g", "l", "?", "tab", "shift+tab", "enter", "[", "]", "esc", "q"}

func TestBindings_CoverEverySpecKey(t *testing.T) {
	for _, want := range specKeys {
		found := false
		for _, b := range Bindings {
			for _, k := range b.Keys {
				if k == want {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("Bindings has no entry for key %q (the design's key table)", want)
		}
	}
}

// TestQuit_WorksEvenInsideAModal is the design's own q row, literally: q
// quits even while a modal is open -- Bindings dispatch (model.go's
// Update) must reach the quit Binding regardless of activeModal/screen
// state.
func TestQuit_WorksEvenInsideAModal(t *testing.T) {
	m := newTestModel(nil)
	m.activeModal = ModalHelp
	m.screen = screenModal

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if _, ok := updated.(*Model); !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("q inside a modal returned a nil cmd, want tea.Quit's Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

// TestQuit_CtrlCAlsoQuits: the same Binding row covers both spellings.
func TestQuit_CtrlCAlsoQuits(t *testing.T) {
	m := newTestModel(nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c returned a nil cmd, want tea.Quit's Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

// TestOpenModal_SetsScreenModalActiveModalAndRemembersPriorScreen covers
// g/l/? uniformly through the shared openModal helper.
func TestOpenModal_SetsScreenModalActiveModalAndRemembersPriorScreen(t *testing.T) {
	cases := []struct {
		key  string
		want ModalKind
	}{
		{"g", ModalGates},
		{"l", ModalLegend},
		{"?", ModalHelp},
	}
	for _, c := range cases {
		m := newTestModel(nil)
		m.screen = screenMain

		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c.key)})
		if m.screen != screenModal {
			t.Errorf("key %q: screen = %v, want screenModal", c.key, m.screen)
		}
		if m.activeModal != c.want {
			t.Errorf("key %q: activeModal = %v, want %v", c.key, m.activeModal, c.want)
		}
		if m.preModalScreen != screenMain {
			t.Errorf("key %q: preModalScreen = %v, want screenMain (remembered)", c.key, m.preModalScreen)
		}
	}
}

// TestEsc_ClosesModalAndRestoresPriorScreen: esc must both close the
// modal AND return to whatever screen was showing before it opened.
func TestEsc_ClosesModalAndRestoresPriorScreen(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.openModal(ModalHelp)
	m.modalScrollOffset = 3

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if m.activeModal != ModalNone {
		t.Errorf("activeModal = %v after esc, want ModalNone", m.activeModal)
	}
	if m.screen != screenMain {
		t.Errorf("screen = %v after esc, want the restored screenMain", m.screen)
	}
	if m.modalScrollOffset != 0 {
		t.Errorf("modalScrollOffset = %d after esc, want reset to 0", m.modalScrollOffset)
	}
}

// TestEsc_NoopAtRoot: with no modal open and no drill-down open, esc is a
// no-op -- it never quits at root.
func TestEsc_NoopAtRoot(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := updated.(*Model)
	if cmd != nil {
		t.Error("esc at root returned a non-nil cmd, want nil (no-op)")
	}
	if mm.screen != screenMain {
		t.Errorf("screen = %v after esc at root, want unchanged screenMain", mm.screen)
	}
}

// TestEsc_ExitsDrillDownToMain covers the design's own screen transition
// table: drill-down is "Exited by: esc" -- Task 4.7's own acceptance
// criterion [design: Task 4.7].
func TestEsc_ExitsDrillDownToMain(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenDrillDown
	m.drillKind = rowListener
	m.drillIndex = 2

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := updated.(*Model)

	if cmd != nil {
		t.Error("esc from drill-down returned a non-nil cmd, want nil")
	}
	if mm.screen != screenMain {
		t.Errorf("screen = %v after esc from drill-down, want screenMain", mm.screen)
	}
}

// TestOpenModal_SwitchingModalsKeepsTheOriginalPriorScreen: opening a
// SECOND modal (e.g. l while ? is already open) must not overwrite
// preModalScreen with the first modal's own screenModal -- esc must
// always land back on the screen from BEFORE any modal opened.
func TestOpenModal_SwitchingModalsKeepsTheOriginalPriorScreen(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.openModal(ModalHelp)
	m.openModal(ModalLegend) // switch modals without an intervening esc

	if m.preModalScreen != screenMain {
		t.Fatalf("preModalScreen = %v after switching modals, want the original screenMain", m.preModalScreen)
	}
	if m.activeModal != ModalLegend {
		t.Fatalf("activeModal = %v, want ModalLegend (the second open)", m.activeModal)
	}
}

// TestStepFocus_CyclesAllThreePanesAndWraps is pg2-ctqpj's own red-first
// test: before the fix, handleFocusNext/handleFocusPrev were literal
// no-ops (`return nil`), so m.focusedPane could never leave its zero value
// (paneListeners) -- Enter could therefore only ever drill into Listeners,
// which is exactly the live bug report's "operator could not select
// anything else ... stuck on one path." tab must visit all three panes in
// the same order renderMain actually renders them (Listeners, Sources,
// Queues -- pg2-ygtwl fixed the pane enum's own declaration order to match
// this, after it had drifted out of sync with the Task 4 two-tier
// redesign) and wrap rather than clamp -- pane focus is a ring, unlike
// stepSibling's row clamp (ux-12). Renamed and narrowed from 4 to 3 panes
// (this docket's Registry-pane removal, Task 2). See also
// TestStepFocus_MatchesRenderMainVisualOrder below, which derives this
// same expectation from renderMain's actual rendered output rather than
// hardcoding it here, so a future re-reorder of renderMain's zones is
// caught even if this literal table is not updated.
func TestStepFocus_CyclesAllThreePanesAndWraps(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	if m.focusedPane != paneListeners {
		t.Fatalf("focusedPane = %v before any tab, want the documented zero-value default paneListeners", m.focusedPane)
	}

	wantForward := []int{paneSources, paneQueues, paneListeners}
	for _, want := range wantForward {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		if cmd != nil {
			t.Errorf("tab returned a non-nil cmd, want nil")
		}
		if m.focusedPane != want {
			t.Fatalf("focusedPane after tab = %v, want %v", m.focusedPane, want)
		}
	}

	wantBackward := []int{paneQueues, paneSources, paneListeners}
	for _, want := range wantBackward {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		if cmd != nil {
			t.Errorf("shift+tab returned a non-nil cmd, want nil")
		}
		if m.focusedPane != want {
			t.Fatalf("focusedPane after shift+tab = %v, want %v", m.focusedPane, want)
		}
	}
}

// TestStepFocus_MatchesRenderMainVisualOrder is pg2-ygtwl's own red-first
// test (the original bug: enum-order tab cycling had drifted out of sync
// with renderMain's actual visual pane order after the Task 4 two-tier
// redesign moved Queues below Sources without stepFocus's own cycle being
// reordered to match). Unlike TestStepFocus_CyclesAllThreePanesAndWraps
// above -- which hardcodes the paneListeners/paneSources/paneQueues
// literals -- this test derives the expected tab order by actually
// rendering screenMain (m.renderMain()) and reading off which pane's box
// title appears on the earliest line, so a FUTURE reorder of renderMain's
// zones (e.g. moving Queues back above Sources) fails THIS test even if
// nobody remembers to touch the pane enum/stepFocus to match.
func TestStepFocus_MatchesRenderMainVisualOrder(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.width = 100
	m.height = 60 // tall enough that no zone -- including the focused fill pane -- drops
	m.reply = StatusReply{
		Listeners: []Listener{{Role: "feedback"}},
		Sources:   []Source{{Name: "queue-src"}},
		Queues:    []Queue{{Type: "pr.review"}},
	}

	rendered := m.renderMain()
	lines := map[int]int{
		paneListeners: lineContaining(rendered, "┌ Listeners"),
		paneSources:   lineContaining(rendered, "┌ Sources"),
		paneQueues:    lineContaining(rendered, "┌ Queues"),
	}
	names := map[int]string{paneListeners: "Listeners", paneSources: "Sources", paneQueues: "Queues"}
	for id, line := range lines {
		if line < 0 {
			t.Fatalf("%s pane box not found in rendered screenMain:\n%s", names[id], rendered)
		}
	}

	visualOrder := []int{paneListeners, paneSources, paneQueues}
	sort.Slice(visualOrder, func(i, j int) bool { return lines[visualOrder[i]] < lines[visualOrder[j]] })

	// tab from Listeners must walk the panes in exactly the ACTUAL visual
	// order derived above, wrapping back to Listeners.
	startIdx := 0
	for i, id := range visualOrder {
		if id == paneListeners {
			startIdx = i
		}
	}
	wantForward := make([]int, 0, len(visualOrder))
	for i := 1; i <= len(visualOrder); i++ {
		wantForward = append(wantForward, visualOrder[(startIdx+i)%len(visualOrder)])
	}

	m2 := newTestModel(nil)
	m2.screen = screenMain
	for _, want := range wantForward {
		m2.stepFocus(1)
		if m2.focusedPane != want {
			t.Fatalf("focusedPane after tab = %v (%s), want %v (%s) -- renderMain's actual visual order was %v",
				m2.focusedPane, names[m2.focusedPane], want, names[want], visualOrder)
		}
	}
}

// TestStepFocus_NoopOutsideScreenMain: pane focus is only ever rendered on
// screenMain (renderPaneContent's "(focused)" title suffix) -- tab/
// shift+tab must not silently mutate focusedPane while a modal or
// drill-down is open, mirroring enterDrillDown's identical
// screenMain-only guard.
func TestStepFocus_NoopOutsideScreenMain(t *testing.T) {
	for _, s := range []screen{screenNoCore, screenQuiescing, screenModal, screenDrillDown, screenLoading} {
		m := newTestModel(nil)
		m.screen = s
		m.focusedPane = paneSources

		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		if m.focusedPane != paneSources {
			t.Errorf("starting from %v: tab changed focusedPane to %v, want unchanged paneSources", s, m.focusedPane)
		}
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		if m.focusedPane != paneSources {
			t.Errorf("starting from %v: shift+tab changed focusedPane to %v, want unchanged paneSources", s, m.focusedPane)
		}
	}
}

// TestOperatorRepro_EnterThenTabThenShiftTab replays the exact key
// sequence from the live walkthrough report (pg2-ctqpj): Enter drills into
// the focused Listeners row, then the operator tries tab and shift+tab to
// select something else. Before the fix this sequence left the operator
// stuck: tab/shift+tab were unconditional no-ops, so returning to
// screenMain (esc) never actually changed which pane Enter would drill
// into next. After the fix: tab/shift+tab still correctly no-op WHILE
// still in drill-down (pane focus has no meaning there), but once back on
// screenMain they move focusedPane, so a second Enter reaches a DIFFERENT
// row (Sources, not Listeners) -- the operator is no longer stuck on one
// path.
func TestOperatorRepro_EnterThenTabThenShiftTab(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.focusedPane = paneListeners
	m.reply = StatusReply{
		Listeners: []Listener{{Role: "feedback"}},
		Sources:   []Source{{Name: "queue-src"}},
	}

	// Enter: drills into Listeners -> feedback (matches the report).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if m.screen != screenDrillDown || m.drillKind != rowListener {
		t.Fatalf("after enter: screen/drillKind = %v/%v, want screenDrillDown/rowListener", m.screen, m.drillKind)
	}

	// tab / shift+tab while still drilled in: no-op (screenMain-only), and
	// crucially must NOT leave the operator any more stuck than before --
	// no panic, no unintended screen change.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(*Model)
	if m.screen != screenDrillDown || m.focusedPane != paneListeners {
		t.Fatalf("after tab/shift+tab inside drill-down: screen/focusedPane = %v/%v, want unchanged screenDrillDown/paneListeners", m.screen, m.focusedPane)
	}

	// esc back to main, then tab once: Listeners -> Sources (the visually
	// next pane down, matching renderMain's actual render order).
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*Model)
	if m.focusedPane != paneSources {
		t.Fatalf("focusedPane after esc+tab = %v, want paneSources (no longer stuck on Listeners)", m.focusedPane)
	}

	// A second Enter now reaches Sources, not Listeners: the operator can
	// select something else.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if m.screen != screenDrillDown || m.drillKind != rowSource {
		t.Fatalf("after second enter: screen/drillKind = %v/%v, want screenDrillDown/rowSource", m.screen, m.drillKind)
	}
}

// TestNoMatchingBinding_IsANoop: a key with no Bindings row is silently
// ignored (Update's own fall-through), never a panic.
func TestNoMatchingBinding_IsANoop(t *testing.T) {
	m := newTestModel(nil)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if cmd != nil {
		t.Error("an unbound key returned a non-nil cmd")
	}
	if _, ok := updated.(*Model); !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
}
