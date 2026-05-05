package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestModelInitialView(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	view := m.View()
	if view == "" {
		t.Error("View must not be empty at init")
	}
}

func TestQuitKey(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestPollResultUpdatesTree(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	updated, _ := m.Update(pollResultMsg{tree: &aggregate.Tree{GeneratedAt: time.Unix(1, 0)}, anyWorking: true})
	mm, ok := updated.(*Model)
	if !ok {
		t.Fatal("cast failed")
	}
	if mm.tree.GeneratedAt.Unix() != 1 {
		t.Errorf("tree not updated: %+v", mm.tree)
	}
}

func TestDownArrowMovesCursor(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a"}},
			{Session: &session.Session{SessionID: "b"}},
		},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	m := NewModel(Options{Tree: tree})
	start := m.cursor
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor == start {
		t.Error("cursor did not advance on down arrow")
	}
}

func makeLargeTree(n int) *aggregate.Tree {
	d := &aggregate.Directory{Path: "/p"}
	for i := range n {
		d.Sessions = append(d.Sessions, &aggregate.SessionView{
			Session: &session.Session{
				SessionID: fmt.Sprintf("id-%d", i),
				Status:    session.Working,
			},
		})
		d.WorkingN++
	}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

func TestSyncScrollAdvancesOffsetWhenCursorExitsViewport(t *testing.T) {
	m := NewModel(Options{Tree: makeLargeTree(20)})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	for range 15 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if m.scrollOffset == 0 {
		t.Errorf("expected scrollOffset > 0 after cursor moves past viewport, cursor=%d", m.cursor)
	}
}

func TestPollTickSkipsDispatchWhilePollInFlight(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	m.polling = true
	_, cmd := m.Update(pollTickMsg{})
	if cmd == nil {
		t.Fatal("expected re-armed tick command, got nil")
	}
	if !m.polling {
		t.Error("polling flag must remain true while a poll is in flight")
	}
}

func TestPollResultClearsPollingFlag(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	m.polling = true
	m.Update(pollResultMsg{tree: &aggregate.Tree{}})
	if m.polling {
		t.Error("polling flag must clear after pollResultMsg")
	}
}

func TestPollErrClearsPollingFlag(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	m.polling = true
	m.Update(pollErrMsg{err: fmt.Errorf("boom")})
	if m.polling {
		t.Error("polling flag must clear after pollErrMsg")
	}
}

func TestSyncScrollReturnsToZeroWhenCursorReturnsToTop(t *testing.T) {
	m := NewModel(Options{Tree: makeLargeTree(20)})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	for range 15 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	for range 15 {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}

	if m.scrollOffset != 0 {
		t.Errorf("expected scrollOffset=0 after cursor returns to top, got %d", m.scrollOffset)
	}
}
