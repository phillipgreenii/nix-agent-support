package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

func TestBindingsAllDocumented(t *testing.T) {
	for i, b := range Bindings {
		if len(b.Keys) == 0 {
			t.Errorf("Bindings[%d] has no Keys", i)
		}
		if b.Description == "" {
			t.Errorf("Bindings[%d] (Keys=%v) missing Description", i, b.Keys)
		}
		if b.Handle == nil {
			t.Errorf("Bindings[%d] (Keys=%v) missing Handle", i, b.Keys)
		}
	}
}

func TestDispatchTViaBindings(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	want := !m.costMode
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if m.costMode != want {
		t.Errorf("pressing t should toggle costMode to %v, got %v", want, m.costMode)
	}
}

func TestDispatchAViaBindings(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	want := !m.showAll
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.showAll != want {
		t.Errorf("pressing a should toggle showAll to %v, got %v", want, m.showAll)
	}
}

func TestDispatchQViaBindings(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Errorf("pressing q should return tea.Quit cmd, got nil")
	}
}
