package tui

import (
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

// TestEsc_NoopAtRoot: with no modal open, esc is a no-op (never quits at
// root; this packet builds no drill-down screen to back out of).
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
