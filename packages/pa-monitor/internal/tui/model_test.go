package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/render"
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

// TestCursorDownStopsAtLastSelectable verifies that pressing Down past the
// last selectable row leaves the cursor on that row (does not roll past or
// land on a BlankKind separator).
func TestCursorDownStopsAtLastSelectable(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
			{Session: &session.Session{SessionID: "s2", Status: session.Working}},
			{Session: &session.Session{SessionID: "s3", Status: session.Working}},
		},
		WorkingN: 3,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})

	for range 50 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if m.cursor < 0 || m.cursor >= len(m.flatRows) {
		t.Fatalf("cursor out of bounds: %d (len=%d)", m.cursor, len(m.flatRows))
	}
	if m.flatRows[m.cursor].Kind == render.BlankKind {
		t.Errorf("cursor parked on BlankKind row at idx=%d", m.cursor)
	}
}

// TestCursorUpStopsAtFirstSelectable verifies the symmetric Up case.
func TestCursorUpStopsAtFirstSelectable(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
			{Session: &session.Session{SessionID: "s2", Status: session.Working}},
		},
		WorkingN: 2,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.cursor = len(m.flatRows) - 1
	m.clampCursor() // ensure starting cursor is on a selectable row

	for range 50 {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}

	if m.flatRows[m.cursor].Kind == render.BlankKind {
		t.Errorf("cursor parked on BlankKind row at idx=%d", m.cursor)
	}
}

// TestClampCursorSnapsOffBlankRow verifies that if external state mutates
// the cursor onto a BlankKind row, clampCursor moves it to the nearest
// selectable row.
func TestClampCursorSnapsOffBlankRow(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
		},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})

	// Find a BlankKind row index, force cursor onto it, then clamp.
	blankIdx := -1
	for i, r := range m.flatRows {
		if r.Kind == render.BlankKind {
			blankIdx = i
			break
		}
	}
	if blankIdx < 0 {
		t.Skip("fixture produced no blank row; clampCursor snap behavior cannot be exercised here")
	}
	m.cursor = blankIdx
	m.clampCursor()

	if m.flatRows[m.cursor].Kind == render.BlankKind {
		t.Errorf("clampCursor failed to snap off blank row; cursor=%d", m.cursor)
	}
}

func TestSignalLogWritesToCacheDirNotStderr(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{CacheDir: dir})
	m.SignalLogForTest("hello world")
	m.SignalLogForTest("second line")
	path := filepath.Join(dir, "signal-errors.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "hello world") || !strings.Contains(got, "second line") {
		t.Errorf("log contents = %q, want both lines", got)
	}
}

type fakeReporter struct {
	pushes   []cmuxstatus.Snapshot
	notifies [][2]string
	clears   int
}

func (f *fakeReporter) Push(s cmuxstatus.Snapshot) { f.pushes = append(f.pushes, s) }
func (f *fakeReporter) Notify(title, body string) {
	f.notifies = append(f.notifies, [2]string{title, body})
}
func (f *fakeReporter) Clear() { f.clears++ }

func TestModelPushesEveryNTicks(t *testing.T) {
	fr := &fakeReporter{}
	m := NewModel(Options{
		Reporter:             fr,
		SidebarIntervalTicks: 3,
	})
	// Drive 5 poll-result messages; expect Push at ticks 3 only (5 ticks total, N=3).
	for i := 0; i < 5; i++ {
		m.Update(PollResultForTest(&aggregate.Tree{}, false))
	}
	if len(fr.pushes) != 1 {
		t.Errorf("expected 1 Push across 5 ticks with N=3, got %d", len(fr.pushes))
	}
}

func TestModelClearsSidebarOnQuit(t *testing.T) {
	fr := &fakeReporter{}
	m := NewModel(Options{Reporter: fr, SidebarIntervalTicks: 5})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if fr.clears != 1 {
		t.Errorf("expected 1 Clear on quit, got %d", fr.clears)
	}
}

func TestModelPushesSidebarOnCaffeinateToggle(t *testing.T) {
	fr := &fakeReporter{}
	m := NewModel(Options{Reporter: fr, SidebarIntervalTicks: 5})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	if len(fr.pushes) != 1 {
		t.Errorf("expected 1 Push on C, got %d", len(fr.pushes))
	}
}

// TestDaemonConnectedDefaultsFalse verifies the daemon-connected flag starts
// false at construction (we haven't talked to the daemon yet).
func TestDaemonConnectedDefaultsFalse(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	if m.daemonConnected {
		t.Error("daemonConnected must default to false before any successful poll")
	}
}

// TestDaemonConnectedSetTrueOnPollResult verifies that a successful poll
// flips the daemon-connected flag true.
func TestDaemonConnectedSetTrueOnPollResult(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	m.Update(pollResultMsg{tree: &aggregate.Tree{}})
	if !m.daemonConnected {
		t.Error("daemonConnected must be true after pollResultMsg")
	}
}

// TestDaemonConnectedSetFalseOnPollErr verifies that a poll error flips the
// daemon-connected flag false even if a previous poll succeeded.
func TestDaemonConnectedSetFalseOnPollErr(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})
	// Establish a connected state first.
	m.Update(pollResultMsg{tree: &aggregate.Tree{}})
	if !m.daemonConnected {
		t.Fatal("precondition: pollResultMsg must set daemonConnected=true")
	}
	// Now simulate the daemon dying mid-session.
	m.Update(pollErrMsg{err: fmt.Errorf("rpc dial failed")})
	if m.daemonConnected {
		t.Error("daemonConnected must be false after pollErrMsg")
	}
}

// TestDaemonConnectedReflectedInView verifies that the indicator glyph
// rendered by the View reflects the current daemonConnected state. We don't
// pin the exact glyph here — just that connected and disconnected views are
// distinguishable.
func TestDaemonConnectedReflectedInView(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}

	mConn := NewModel(Options{Tree: tree})
	mConn.SetSizeForTest(100, 24)
	mConn.Update(pollResultMsg{tree: tree})
	connectedView := mConn.View()

	mOff := NewModel(Options{Tree: tree})
	mOff.SetSizeForTest(100, 24)
	mOff.Update(pollErrMsg{err: fmt.Errorf("offline")})
	offlineView := mOff.View()

	if connectedView == offlineView {
		t.Errorf("connected and offline views must differ to signal daemon state, got identical:\n%s", connectedView)
	}
}
