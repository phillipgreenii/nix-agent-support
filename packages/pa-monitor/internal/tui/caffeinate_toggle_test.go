package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestHandleToggleCaffeinate_FiresCallback confirms that OnCaffeinateToggle
// receives the desired new state on each C keypress and the returned Cmd
// is propagated. Each press toggles relative to the current caffeinateOn
// (driven by Update applying CaffeinateResultMsg in real flows; in this
// test we manually mutate the model to simulate it).
func TestHandleToggleCaffeinate_FiresCallback(t *testing.T) {
	var calls []bool
	m := NewModel(Options{
		Tree: &aggregate.Tree{},
		OnCaffeinateToggle: func(want bool) tea.Cmd {
			calls = append(calls, want)
			return nil
		},
	})

	handleToggleCaffeinate(m)
	if len(calls) != 1 || calls[0] != true {
		t.Errorf("first toggle: got calls=%+v, want [true]", calls)
	}

	// Simulate the daemon committing the new state (what Update would do on
	// CaffeinateResultMsg) so the second press computes the next desired
	// state from a fresh base.
	m.caffeinateOn = true

	handleToggleCaffeinate(m)
	if len(calls) != 2 || calls[1] != false {
		t.Errorf("second toggle: got calls=%+v, want [true,false]", calls)
	}
}

// TestHandleToggleCaffeinate_NilCallbackIsSafe: with no callback wired
// (test / non-remote contexts) the handler falls back to a local flip so
// the keybinding still has an effect.
func TestHandleToggleCaffeinate_NilCallbackIsSafe(t *testing.T) {
	m := NewModel(Options{
		Tree: &aggregate.Tree{},
		// No OnCaffeinateToggle — must not panic.
	})
	handleToggleCaffeinate(m)
	if !m.caffeinateOn {
		t.Error("local toggle should still flip in nil-callback fallback")
	}
}
