package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestBindings_HasProblemsKey pins the "!" keybinding this packet chose
// (bead pg2-5l2he left the exact key as an implementation decision):
// mirrors attentionLine's own "! " marker (liveness.go), so the glyph that
// already flags "something needs attention" on the banner is the same key
// that opens the detail behind it.
func TestBindings_HasProblemsKey(t *testing.T) {
	found := false
	for _, b := range Bindings {
		for _, k := range b.Keys {
			if k == "!" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal(`Bindings has no entry for "!" (the Problems modal key)`)
	}
}

// TestOpenProblems_KeySetsScreenModalActiveModalAndRemembersPriorScreen
// mirrors keybindings_test.go's own TestOpenModal_... coverage for g/l/?,
// extended to the new "!" key: it must open screenModal with
// activeModal == ModalProblems and remember the screen active beforehand,
// via the same shared openModal helper every other modal key uses.
func TestOpenProblems_KeySetsScreenModalActiveModalAndRemembersPriorScreen(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if m.screen != screenModal {
		t.Errorf("screen = %v, want screenModal", m.screen)
	}
	if m.activeModal != ModalProblems {
		t.Errorf("activeModal = %v, want ModalProblems", m.activeModal)
	}
	if m.preModalScreen != screenMain {
		t.Errorf("preModalScreen = %v, want screenMain (remembered)", m.preModalScreen)
	}
}

// TestRenderModal_ProblemsRoutesToOwnContent mirrors help_test.go's own
// TestRenderModal_LegendAndGatesRouteToTheirOwnContent: renderModal must
// actually dispatch ModalProblems to renderProblemsModal, not fall through
// to the default empty case.
func TestRenderModal_ProblemsRoutesToOwnContent(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.activeModal = ModalProblems

	got := m.renderModal()
	if !strings.Contains(got, "Problems") {
		t.Errorf("ModalProblems renderModal() = %q, want it to route through renderProblemsModal", got)
	}
}

// TestRenderProblemsModal_ListsUnmatchedBindingsByName is the bead's first
// "at minimum" item: unmatched-binding types with no single-row home
// elsewhere must be named individually, not just counted.
func TestRenderProblemsModal_ListsUnmatchedBindingsByName(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.reply = StatusReply{UnmatchedBindings: []string{"bead.new", "bead.updated"}}

	got := m.renderProblemsModal()
	for _, want := range []string{"bead.new", "bead.updated"} {
		if !strings.Contains(got, want) {
			t.Errorf("Problems modal missing unmatched binding %q; got:\n%s", want, got)
		}
	}
}

// TestRenderProblemsModal_NoUnmatchedBindingsIsUnambiguous: an empty
// UnmatchedBindings must render the explicit "(none)", never a silently
// absent section -- the same "confirmed fact, not missing data" contract
// gateModalRow's "not set" wording already established (pg2-y6sy5).
func TestRenderProblemsModal_NoUnmatchedBindingsIsUnambiguous(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.reply = StatusReply{}

	got := m.renderProblemsModal()
	if !strings.Contains(got, "(none)") {
		t.Errorf("Problems modal with no unmatched bindings should show \"(none)\"; got:\n%s", got)
	}
}

// TestRenderProblemsModal_ListsBothGatesByName is the bead's second
// "at minimum" item: current gate state, reusing gateModalRow (gates.go) so
// this view and the Gates modal never drift into two different renderings
// of the same fact.
func TestRenderProblemsModal_ListsBothGatesByName(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.reply = StatusReply{Gates: []Gate{
		{Name: core.GateOperatorPaused, Set: true, Owner: "operator"},
		// cicd_down deliberately absent -- never observed yet.
	}}

	got := m.renderProblemsModal()
	for _, want := range []string{"operator-paused", "cicd-down", "SET", "not set", "operator"} {
		if !strings.Contains(got, want) {
			t.Errorf("Problems modal missing %q; got:\n%s", want, got)
		}
	}
}

// TestRenderProblemsModal_ListsRecentErrorLogEntries is the bead's third
// "at minimum" item: tui-errors.log becomes viewable from inside the TUI
// for the first time.
func TestRenderProblemsModal_ListsRecentErrorLogEntries(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{CacheDir: dir}, render.NewTheme(false))
	m.width, m.height = 80, 24
	m.errorLogger.LogString("gate toggle failed: dial: no running core")
	m.errorLogger.LogString("something else went wrong")

	got := m.renderProblemsModal()
	for _, want := range []string{"tui-errors.log", "gate toggle failed", "something else went wrong"} {
		if !strings.Contains(got, want) {
			t.Errorf("Problems modal missing %q; got:\n%s", want, got)
		}
	}
}

// TestRenderProblemsModal_NoErrorsLoggedYetIsUnambiguous: no CacheDir (and
// so no ErrorLogger, no file) must render the explicit
// "(no errors logged yet)", never a blank section.
func TestRenderProblemsModal_NoErrorsLoggedYetIsUnambiguous(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24

	got := m.renderProblemsModal()
	if !strings.Contains(got, "(no errors logged yet)") {
		t.Errorf("Problems modal with no error log should say so explicitly; got:\n%s", got)
	}
}

// TestProblemsModalFooter_NamesFullLogPath: an operator who wants more than
// problemsErrorLogTailLines' worth of history is told exactly where to find
// the raw file -- the same path helpFooter (help.go) already names.
func TestProblemsModalFooter_NamesFullLogPath(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{CacheDir: dir}, render.NewTheme(false))

	got := m.problemsModalFooter()
	if !strings.Contains(got, errorLogPath(dir)) {
		t.Errorf("problemsModalFooter() = %q, want it to name %q", got, errorLogPath(dir))
	}
}

// TestProblemsModalFooter_EmptyCacheDirOmitsPathLine mirrors helpFooter's
// own identical guard: nothing to point to when Options.CacheDir was never
// set.
func TestProblemsModalFooter_EmptyCacheDirOmitsPathLine(t *testing.T) {
	m := newTestModel(nil)
	if got := m.problemsModalFooter(); got != "" {
		t.Errorf("problemsModalFooter() = %q, want empty with no CacheDir set", got)
	}
}

// TestRenderProblemsModal_AdditiveNotReplacement: opening the Problems modal
// must not disturb the Gates modal's own rendering, and vice versa --
// bead pg2-5l2he's own explicit acceptance criterion that this view is a
// superset/detail view, not a replacement.
func TestRenderProblemsModal_AdditiveNotReplacement(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.reply = StatusReply{Gates: []Gate{{Name: core.GateOperatorPaused, Set: true, Owner: "operator"}}}

	gates := m.renderGatesModal()
	problems := m.renderProblemsModal()

	if !strings.Contains(gates, "operator-paused") {
		t.Fatalf("renderGatesModal() no longer lists operator-paused; got:\n%s", gates)
	}
	if !strings.Contains(problems, "operator-paused") {
		t.Fatalf("renderProblemsModal() does not list operator-paused; got:\n%s", problems)
	}
	if strings.Contains(gates, "Problems") {
		t.Errorf("renderGatesModal() unexpectedly carries the Problems modal's own title; got:\n%s", gates)
	}
}

// --- tailErrorLog (errorlog.go) ---

// TestTailErrorLog_ReturnsLastNLinesOldestFirst: with more lines logged than
// requested, the most recent n come back in chronological (oldest-of-the-
// tail-first) order -- the same convention reply.Activity already uses.
func TestTailErrorLog_ReturnsLastNLinesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "tui-errors.log"}
	for _, line := range []string{"one", "two", "three", "four", "five"} {
		l.LogString(line)
	}

	got := tailErrorLog(dir, 3)
	want := []string{"three", "four", "five"}
	if len(got) != len(want) {
		t.Fatalf("tailErrorLog returned %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q (got %v)", i, got[i], want[i], got)
		}
	}
}

// TestTailErrorLog_FewerLinesThanRequestedReturnsAll: asking for more lines
// than exist returns everything, not an error or a padded/truncated result.
func TestTailErrorLog_FewerLinesThanRequestedReturnsAll(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "tui-errors.log"}
	l.LogString("only one line")

	got := tailErrorLog(dir, 10)
	if len(got) != 1 || got[0] != "only one line" {
		t.Fatalf("tailErrorLog = %v, want exactly [\"only one line\"]", got)
	}
}

// TestTailErrorLog_EmptyCacheDirReturnsNil: nothing to read, nothing to
// return -- mirrors ErrorLogger.LogString's own empty-CacheDir no-op.
func TestTailErrorLog_EmptyCacheDirReturnsNil(t *testing.T) {
	if got := tailErrorLog("", 10); got != nil {
		t.Errorf("tailErrorLog(\"\", 10) = %v, want nil", got)
	}
}

// TestTailErrorLog_MissingFileReturnsNil: a CacheDir that has never had
// anything logged to it (no tui-errors.log created yet) is not an error.
func TestTailErrorLog_MissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if got := tailErrorLog(dir, 10); got != nil {
		t.Errorf("tailErrorLog on a dir with no log file = %v, want nil", got)
	}
}

// TestTailErrorLog_ZeroOrNegativeNReturnsNil: an invalid request line count
// returns nothing rather than panicking or reading the whole file.
func TestTailErrorLog_ZeroOrNegativeNReturnsNil(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "tui-errors.log"}
	l.LogString("x")

	if got := tailErrorLog(dir, 0); got != nil {
		t.Errorf("tailErrorLog(dir, 0) = %v, want nil", got)
	}
	if got := tailErrorLog(dir, -1); got != nil {
		t.Errorf("tailErrorLog(dir, -1) = %v, want nil", got)
	}
}

// TestTailErrorLog_CorrectAcrossReadBoundaryTruncation regression-tests the
// tailErrorLogMaxReadBytes bound: when the file is far larger than the read
// window, the read starts mid-file and the first "line" read back is a
// possibly-truncated fragment -- it must be dropped, and the LAST n lines
// (the ones actually requested) must still come back byte-for-byte intact,
// not corrupted by the boundary.
func TestTailErrorLog_CorrectAcrossReadBoundaryTruncation(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "tui-errors.log"}

	// Enough ~30-byte lines to comfortably exceed tailErrorLogMaxReadBytes
	// (64KiB), forcing tailErrorLog's ReadAt to start mid-file.
	total := (tailErrorLogMaxReadBytes / 20) + 500
	for i := 0; i < total; i++ {
		l.LogString(strings.Repeat("x", 20) + "-" + strconv.Itoa(i))
	}

	got := tailErrorLog(dir, 5)
	if len(got) != 5 {
		t.Fatalf("tailErrorLog returned %d lines, want 5: %v", len(got), got)
	}
	for i, want := range []int{total - 5, total - 4, total - 3, total - 2, total - 1} {
		wantLine := strings.Repeat("x", 20) + "-" + strconv.Itoa(want)
		if got[i] != wantLine {
			t.Errorf("line %d = %q, want %q", i, got[i], wantLine)
		}
	}
}
