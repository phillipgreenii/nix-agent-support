package tui

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestHandleToggleCaffeinate_FiresCallback confirms that the OnCaffeinateToggle
// hook in Options receives the new state on each C keypress. In --remote
// mode this hook dispatches the Caffeinate RPC against the daemon.
func TestHandleToggleCaffeinate_FiresCallback(t *testing.T) {
	var calls []bool
	m := NewModel(Options{
		Tree: &aggregate.Tree{},
		OnCaffeinateToggle: func(on bool) {
			calls = append(calls, on)
		},
	})

	handleToggleCaffeinate(m)
	if len(calls) != 1 || calls[0] != true {
		t.Errorf("first toggle: got calls=%+v, want [true]", calls)
	}

	handleToggleCaffeinate(m)
	if len(calls) != 2 || calls[1] != false {
		t.Errorf("second toggle: got calls=%+v, want [true,false]", calls)
	}
}

func TestHandleToggleCaffeinate_NilCallbackIsSafe(t *testing.T) {
	m := NewModel(Options{
		Tree: &aggregate.Tree{},
		// No OnCaffeinateToggle — must not panic.
	})
	handleToggleCaffeinate(m)
	if !m.caffeinateOn {
		t.Error("local toggle should still flip even without callback")
	}
}
