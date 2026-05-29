package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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

// --- R keybind (toggle auto-resume) ---

func TestKeybindR_TogglesAutoResumeOptimistically(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.autoResumeEnabled = false
	handleToggleAutoResume(m)
	if !m.autoResumeEnabled {
		t.Error("pressing R should flip autoResumeEnabled true (optimistic update)")
	}
	handleToggleAutoResume(m)
	if m.autoResumeEnabled {
		t.Error("pressing R again should flip autoResumeEnabled back to false")
	}
}

func TestKeybindR_FiresCallback(t *testing.T) {
	var calls []bool
	m := NewModel(Options{
		Tree:               &aggregate.Tree{},
		OnToggleAutoResume: func(enable bool) { calls = append(calls, enable) },
	})
	m.autoResumeEnabled = false

	handleToggleAutoResume(m)
	if len(calls) != 1 || !calls[0] {
		t.Errorf("first R: want calls=[true], got %v", calls)
	}

	handleToggleAutoResume(m)
	if len(calls) != 2 || calls[1] {
		t.Errorf("second R: want calls=[true,false], got %v", calls)
	}
}

func TestKeybindR_NilCallbackIsSafe(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	// Must not panic when OnToggleAutoResume is nil.
	handleToggleAutoResume(m)
	if !m.autoResumeEnabled {
		t.Error("autoResumeEnabled should flip even without callback")
	}
}

// --- M keybind (manual nudge toggle) ---

// makeSessionTree builds a Tree with one directory at the given path and one
// session with the supplied ID and optional PendingNudge.
func makeSessionTree(dirPath, sid string, nudge *aggregate.PendingNudge) *aggregate.Tree {
	sv := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: sid,
			Status:    session.Idle,
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			PendingNudge: nudge,
		},
	}
	d := &aggregate.Directory{Path: dirPath, Sessions: []*aggregate.SessionView{sv}}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

func TestKeybindN_QueuesManualOnLeafSession(t *testing.T) {
	var gotSelector string
	var gotCancel bool
	var called bool

	tree := makeSessionTree("/proj/a", "sid-1", nil)
	m := NewModel(Options{
		Tree: tree,
		OnManualNudge: func(selector string, cancel bool) {
			gotSelector = selector
			gotCancel = cancel
			called = true
		},
	})
	// flatRows: PathNodeKind(0), SessionKind(1), BlankKind(2)
	// Cursor on the SessionKind row.
	m.cursor = 1

	handleManualResume(m)

	if !called {
		t.Fatal("OnManualNudge callback was not called")
	}
	if gotSelector != "session:sid-1" {
		t.Errorf("selector = %q, want %q", gotSelector, "session:sid-1")
	}
	if gotCancel {
		t.Error("cancel should be false when session has no pending manual nudge")
	}
}

func TestKeybindN_CancelsWhenAllPending(t *testing.T) {
	var gotCancel bool
	var called bool

	nudge := &aggregate.PendingNudge{Sources: []string{"manual"}}
	tree := makeSessionTree("/proj/b", "sid-2", nudge)
	m := NewModel(Options{
		Tree: tree,
		OnManualNudge: func(selector string, cancel bool) {
			gotCancel = cancel
			called = true
		},
	})
	m.cursor = 1

	handleManualResume(m)

	if !called {
		t.Fatal("OnManualNudge callback was not called")
	}
	if !gotCancel {
		t.Error("cancel should be true when session already has a manual pending nudge")
	}
}

func TestKeybindN_PathNodeUsesPathSelector(t *testing.T) {
	var gotSelector string
	var called bool

	tree := makeSessionTree("/proj/c", "sid-3", nil)
	m := NewModel(Options{
		Tree: tree,
		OnManualNudge: func(selector string, cancel bool) {
			gotSelector = selector
			called = true
		},
	})
	// flatRows: PathNodeKind(0), SessionKind(1), BlankKind(2)
	// Cursor on the PathNodeKind row.
	m.cursor = 0

	handleManualResume(m)

	if !called {
		t.Fatal("OnManualNudge callback was not called")
	}
	wantPrefix := "path:"
	if len(gotSelector) < len(wantPrefix) || gotSelector[:len(wantPrefix)] != wantPrefix {
		t.Errorf("selector = %q, want prefix %q", gotSelector, wantPrefix)
	}
}

func TestKeybindN_NilCallbackIsSafe(t *testing.T) {
	tree := makeSessionTree("/proj/d", "sid-4", nil)
	m := NewModel(Options{Tree: tree})
	m.cursor = 1
	// Must not panic when OnManualNudge is nil.
	handleManualResume(m)
}

func TestSessionHasPendingManual_TrueWhenSourcePresent(t *testing.T) {
	nudge := &aggregate.PendingNudge{Sources: []string{"window_reset", "manual"}}
	tree := makeSessionTree("/proj/e", "sid-5", nudge)
	m := NewModel(Options{Tree: tree})
	if !m.sessionHasPendingManual("sid-5") {
		t.Error("expected sessionHasPendingManual=true when 'manual' is in Sources")
	}
}

func TestSessionHasPendingManual_FalseWhenSourceAbsent(t *testing.T) {
	nudge := &aggregate.PendingNudge{Sources: []string{"window_reset"}}
	tree := makeSessionTree("/proj/f", "sid-6", nudge)
	m := NewModel(Options{Tree: tree})
	if m.sessionHasPendingManual("sid-6") {
		t.Error("expected sessionHasPendingManual=false when 'manual' is not in Sources")
	}
}

func TestSessionHasPendingManual_FalseWhenNilNudge(t *testing.T) {
	tree := makeSessionTree("/proj/g", "sid-7", nil)
	m := NewModel(Options{Tree: tree})
	if m.sessionHasPendingManual("sid-7") {
		t.Error("expected sessionHasPendingManual=false when PendingNudge is nil")
	}
}
