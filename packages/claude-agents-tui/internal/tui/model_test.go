package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
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

func TestRebuildFlatRowsBuildsOnPollResult(t *testing.T) {
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "a", Status: session.Working}}},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(pollResultMsg{tree: tree})
	if len(m.flatRows) == 0 {
		t.Error("flatRows should be populated after pollResultMsg")
	}
}

func TestClampCursorUsesAllRows(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.cursor = 999
	m.clampCursor()
	if m.cursor >= len(m.flatRows) {
		t.Errorf("cursor should be clamped to flatRows length, got %d (len=%d)", m.cursor, len(m.flatRows))
	}
}

func TestRowAtReturnsCorrectRow(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
		},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	// flatRows: PathNodeKind(0), SessionKind(1), BlankKind(2)
	row, ok := m.rowAt(0)
	if !ok {
		t.Fatal("rowAt(0) should return a row")
	}
	if row.Kind != render.PathNodeKind {
		t.Errorf("rows[0] should be PathNodeKind, got %v", row.Kind)
	}
	if _, ok := m.rowAt(999); ok {
		t.Error("rowAt out of bounds should return ok=false")
	}
}

// mockSignaler records Send calls for test assertions.
type mockSignaler struct {
	name   string
	detect bool
	sent   []string
}

func (m *mockSignaler) Name() string              { return m.name }
func (m *mockSignaler) Detect(_ int) bool         { return m.detect }
func (m *mockSignaler) Send(pid int, text string) error {
	m.sent = append(m.sent, fmt.Sprintf("%d:%s", pid, text))
	return nil
}

func TestAutoResumeFireSignalsIdleSessions(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	resetsAt := time.Now().Add(-1 * time.Minute)
	tree := &aggregate.Tree{
		WindowResetsAt: resetsAt,
		Dirs: []*aggregate.Directory{{
			Path: "/p",
			Sessions: []*aggregate.SessionView{{
				Session:           &session.Session{PID: 42, Status: session.Idle},
				SessionEnrichment: aggregate.SessionEnrichment{RateLimitResetsAt: resetsAt},
			}},
		}},
	}
	m := NewModel(Options{
		Tree:              tree,
		Signalers:         []signal.Signaler{mock},
		AutoResumeDelay:   0,
		AutoResumeMessage: "continue",
	})
	m.autoResume = true
	m.Update(autoResumeFireMsg{})
	if len(mock.sent) != 1 || mock.sent[0] != "42:continue" {
		t.Errorf("sent = %v, want [\"42:continue\"]", mock.sent)
	}
	if !m.tree.WindowResetsAt.IsZero() {
		t.Error("WindowResetsAt should be cleared after fire")
	}
	if !m.autoResumeFired {
		t.Error("autoResumeFired should be true after fire")
	}
}

func TestAutoResumeFireSkipsWorkingSessions(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	resetsAt := time.Now().Add(-1 * time.Minute)
	tree := &aggregate.Tree{
		WindowResetsAt: resetsAt,
		Dirs: []*aggregate.Directory{{
			Path: "/p",
			Sessions: []*aggregate.SessionView{{
				Session:           &session.Session{PID: 99, Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{},
			}},
		}},
	}
	m := NewModel(Options{
		Tree:              tree,
		Signalers:         []signal.Signaler{mock},
		AutoResumeDelay:   0,
		AutoResumeMessage: "continue",
	})
	m.autoResume = true
	m.Update(autoResumeFireMsg{})
	if len(mock.sent) != 0 {
		t.Errorf("sent = %v, want empty (Working session must be skipped)", mock.sent)
	}
}

func TestAutoResumeFireIgnoredWhenAutoResumeOff(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	resetsAt := time.Now().Add(-1 * time.Minute)
	tree := &aggregate.Tree{
		WindowResetsAt: resetsAt,
		Dirs: []*aggregate.Directory{{
			Path: "/p",
			Sessions: []*aggregate.SessionView{{
				Session: &session.Session{PID: 42, Status: session.Idle},
			}},
		}},
	}
	m := NewModel(Options{Tree: tree, Signalers: []signal.Signaler{mock}})
	m.autoResume = false
	m.Update(autoResumeFireMsg{})
	if len(mock.sent) != 0 {
		t.Errorf("sent = %v, want empty (autoResume off)", mock.sent)
	}
}

func TestRToggleEnablesAutoResume(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	if m.autoResume {
		t.Fatal("autoResume should start false")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if !m.autoResume {
		t.Error("autoResume should be true after R key")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if m.autoResume {
		t.Error("autoResume should toggle back to false on second R")
	}
}

func TestAutoResumeFiredResetWhenWindowClears(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	resetsAt := time.Now().Add(-1 * time.Minute)
	m.tree = &aggregate.Tree{WindowResetsAt: resetsAt}
	m.autoResumeFired = true
	m.Update(pollResultMsg{tree: &aggregate.Tree{}, anyWorking: false})
	if m.autoResumeFired {
		t.Error("autoResumeFired should reset when WindowResetsAt clears")
	}
}

func TestManualFireSendsToAllNonWorkingRegardlessOfAutoResume(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{{
			Path: "/p",
			Sessions: []*aggregate.SessionView{
				{Session: &session.Session{PID: 11, Status: session.Idle}},
				{Session: &session.Session{PID: 22, Status: session.Working}},
				{Session: &session.Session{PID: 33, Status: session.Idle}},
			},
		}},
	}
	m := NewModel(Options{
		Tree:              tree,
		Signalers:         []signal.Signaler{mock},
		AutoResumeMessage: "go",
	})
	// autoResume left false on purpose — manual fire MUST work without the toggle.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if len(mock.sent) != 2 {
		t.Fatalf("sent = %v, want 2 entries (PIDs 11 and 33)", mock.sent)
	}
	want := map[string]bool{"11:go": false, "33:go": false}
	for _, s := range mock.sent {
		if _, ok := want[s]; !ok {
			t.Errorf("unexpected send %q", s)
		}
		want[s] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing send %q", k)
		}
	}
}

func TestManualFireDoesNotMutateTree(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	resetsAt := time.Now().Add(30 * time.Minute)
	tree := &aggregate.Tree{
		WindowResetsAt: resetsAt,
		Dirs: []*aggregate.Directory{{
			Path:     "/p",
			Sessions: []*aggregate.SessionView{{Session: &session.Session{PID: 7, Status: session.Idle}}},
		}},
	}
	m := NewModel(Options{
		Tree: tree, Signalers: []signal.Signaler{mock}, AutoResumeMessage: "go",
	})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if !m.tree.WindowResetsAt.Equal(resetsAt) {
		t.Errorf("WindowResetsAt mutated; got %v, want %v", m.tree.WindowResetsAt, resetsAt)
	}
	if m.autoResumeFired {
		t.Error("autoResumeFired must not be set by manual fire")
	}
}

func TestManualFireNilTreeNoCrash(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	m := NewModel(Options{Tree: nil, Signalers: []signal.Signaler{mock}, AutoResumeMessage: "go"})
	// Defensive: must not panic, must not send anything.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if len(mock.sent) != 0 {
		t.Errorf("sent = %v, want empty (nil tree)", mock.sent)
	}
}
