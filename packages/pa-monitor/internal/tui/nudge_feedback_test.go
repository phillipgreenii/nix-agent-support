package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// makeStatusTree builds a Tree with one directory holding a single session at
// the supplied ID and status. Mirrors makeSessionTree (keybindings_test.go)
// but lets the caller pick the status so the "working → suppressed" surface
// can be exercised.
func makeStatusTree(dirPath, sid string, status session.Status) *aggregate.Tree {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: sid, Status: status},
	}
	d := &aggregate.Directory{Path: dirPath, Sessions: []*aggregate.SessionView{sv}}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

// makeBlockedTree builds a single-session tree Blocked on the given blocker
// (ADR 0024: WaitingForHuman is now Blocked + a human_* blocker).
func makeBlockedTree(dirPath, sid string, blocker session.Blocker) *aggregate.Tree {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: sid, Status: session.Blocked, Blocker: blocker},
	}
	d := &aggregate.Directory{Path: dirPath, Sessions: []*aggregate.SessionView{sv}}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

func TestNudgeResultMsg_SurfacesQueued(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	if m.nudgeFlash == "" {
		t.Fatal("expected a nudge flash after a queued result")
	}
	if !strings.Contains(m.nudgeFlash, "queued") {
		t.Errorf("flash %q should mention 'queued'", m.nudgeFlash)
	}
	if m.nudgeFlashLevel != flashInfo {
		t.Errorf("queued-to-idle flash should be flashInfo, got %v", m.nudgeFlashLevel)
	}
}

func TestNudgeResultMsg_SurfacesNoMatch(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(NudgeResultMsg{})
	if !strings.Contains(m.nudgeFlash, "no sessions matched") {
		t.Errorf("empty result flash %q should say 'no sessions matched'", m.nudgeFlash)
	}
	if m.nudgeFlashLevel != flashWarn {
		t.Errorf("no-match flash should be flashWarn, got %v", m.nudgeFlashLevel)
	}
}

// TestNudgeResultMsg_SurfacesWorkingSuppression is the core-defect regression:
// a manual nudge that queues against a session the daemon reports as Working
// will be suppressed (session_active) at dispatch time, so the TUI must warn
// the user rather than imply it was delivered.
func TestNudgeResultMsg_SurfacesWorkingSuppression(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Working)})
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	if !strings.Contains(m.nudgeFlash, "working") {
		t.Errorf("flash %q should warn that the session is working", m.nudgeFlash)
	}
	if !strings.Contains(m.nudgeFlash, "suppress") {
		t.Errorf("flash %q should warn about suppression", m.nudgeFlash)
	}
	if m.nudgeFlashLevel != flashWarn {
		t.Errorf("working-suppression flash should be flashWarn, got %v", m.nudgeFlashLevel)
	}
}

// TestNudgeResultMsg_SurfacesWaitingForHumanSuppression is the pg2-gweng
// regression: the daemon dispatcher suppresses manual nudges for
// WaitingForHuman (waiting_for_human) symmetrically with Working
// (session_active), so a queue against a WaitingForHuman session must WARN
// rather than flash a neutral "queued" that reads as delivered.
func TestNudgeResultMsg_SurfacesWaitingForHumanSuppression(t *testing.T) {
	m := NewModel(Options{Tree: makeBlockedTree("/proj/a", "sid-1", session.HumanInput)})
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	if !strings.Contains(m.nudgeFlash, "waiting for human") {
		t.Errorf("flash %q should warn the session is waiting for human", m.nudgeFlash)
	}
	if !strings.Contains(m.nudgeFlash, "suppress") {
		t.Errorf("flash %q should warn about suppression", m.nudgeFlash)
	}
	if m.nudgeFlashLevel != flashWarn {
		t.Errorf("waiting-for-human suppression flash should be flashWarn, got %v", m.nudgeFlashLevel)
	}
}

// TestNudgeResultMsg_MixedSuppressionWarns proves a queue spanning a Working
// and a WaitingForHuman session names both suppression reasons at WARN level.
func TestNudgeResultMsg_MixedSuppressionWarns(t *testing.T) {
	sv1 := &aggregate.SessionView{Session: &session.Session{SessionID: "sid-w", Status: session.Working}}
	sv2 := &aggregate.SessionView{Session: &session.Session{SessionID: "sid-h", Status: session.Blocked, Blocker: session.HumanInput}}
	d := &aggregate.Directory{Path: "/proj/a", Sessions: []*aggregate.SessionView{sv1, sv2}}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.Update(NudgeResultMsg{Queued: []string{"sid-w", "sid-h"}})
	if !strings.Contains(m.nudgeFlash, "1 working") || !strings.Contains(m.nudgeFlash, "1 waiting for human") {
		t.Errorf("flash %q should name both suppression reasons", m.nudgeFlash)
	}
	if m.nudgeFlashLevel != flashWarn {
		t.Errorf("mixed-suppression flash should be flashWarn, got %v", m.nudgeFlashLevel)
	}
}

// TestNudgeResultMsg_IdleQueuedStaysInfo guards the negative: a queue against a
// purely-idle session (the dispatcher will deliver it) must stay neutral info,
// not warn.
func TestNudgeResultMsg_IdleQueuedStaysInfo(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	if m.nudgeFlashLevel != flashInfo {
		t.Errorf("idle queued flash should be flashInfo, got %v", m.nudgeFlashLevel)
	}
	if strings.Contains(m.nudgeFlash, "suppress") {
		t.Errorf("idle queued flash %q should not mention suppression", m.nudgeFlash)
	}
}

func TestNudgeResultMsg_SurfacesAlreadyQueued(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.Update(NudgeResultMsg{Already: []string{"sid-1"}})
	if !strings.Contains(m.nudgeFlash, "already") {
		t.Errorf("flash %q should mention 'already'", m.nudgeFlash)
	}
}

func TestNudgeResultMsg_SurfacesCancel(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.Update(NudgeResultMsg{Cancel: true, Cancelled: []string{"sid-1"}})
	if !strings.Contains(m.nudgeFlash, "cancelled") {
		t.Errorf("flash %q should mention 'cancelled'", m.nudgeFlash)
	}

	m.Update(NudgeResultMsg{Cancel: true})
	if !strings.Contains(m.nudgeFlash, "nothing") {
		t.Errorf("empty-cancel flash %q should say nothing was queued", m.nudgeFlash)
	}
}

func TestNudgeErrMsg_SurfacesFailure(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(NudgeErrMsg{Err: errFake("daemon unreachable")})
	if !strings.Contains(m.nudgeFlash, "daemon unreachable") {
		t.Errorf("flash %q should carry the RPC error", m.nudgeFlash)
	}
	if m.nudgeFlashLevel != flashWarn {
		t.Errorf("error flash should be flashWarn, got %v", m.nudgeFlashLevel)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

func TestNudgeFlashClears_AfterTTL(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	if m.nudgeFlash == "" {
		t.Fatal("expected a flash to be set")
	}
	// Simulate the TTL having elapsed, then deliver the clear tick.
	m.nudgeFlashUntil = time.Now().Add(-time.Second)
	m.Update(nudgeFlashClearMsg{})
	if m.nudgeFlash != "" {
		t.Errorf("expired flash should have been cleared, still %q", m.nudgeFlash)
	}
}

func TestNudgeFlashClear_KeepsUnexpiredFlash(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	// A stale clear tick (flash re-set more recently) must not wipe the flash.
	m.Update(nudgeFlashClearMsg{})
	if m.nudgeFlash == "" {
		t.Error("unexpired flash should survive a clear tick")
	}
}

// TestFooterRendersNudgeFlash confirms the outcome is actually visible in the
// rendered footer (not merely stored on the model).
func TestFooterRendersNudgeFlash(t *testing.T) {
	m := NewModel(Options{Tree: makeStatusTree("/proj/a", "sid-1", session.Idle)})
	m.SetSizeForTest(120, 20)
	m.Update(NudgeResultMsg{Queued: []string{"sid-1"}})
	out := m.View()
	if !strings.Contains(out, "queued") {
		t.Errorf("rendered view should show the nudge flash; got:\n%s", out)
	}
}

// TestHandleManualResumeReturnsCallbackCmd verifies the N handler now threads
// the callback's tea.Cmd back to the Update loop (so the RPC outcome can be
// surfaced) instead of firing-and-forgetting.
func TestHandleManualResumeReturnsCallbackCmd(t *testing.T) {
	sentinel := NudgeResultMsg{Queued: []string{"sid-1"}}
	m := NewModel(Options{
		Tree: makeStatusTree("/proj/a", "sid-1", session.Idle),
		OnManualNudge: func(selector string, cancel bool) tea.Cmd {
			return func() tea.Msg { return sentinel }
		},
	})
	m.cursor = 1 // SessionKind row
	cmd := handleManualResume(m)
	if cmd == nil {
		t.Fatal("handleManualResume should return the callback's cmd")
	}
	if _, ok := cmd().(NudgeResultMsg); !ok {
		t.Errorf("callback cmd should yield a NudgeResultMsg")
	}
}

func TestHandleManualResume_NoSessionUnderCursorSurfaced(t *testing.T) {
	// Empty tree → no selectable session; pressing N should flash rather than
	// silently no-op.
	m := NewModel(Options{
		Tree:          &aggregate.Tree{},
		OnManualNudge: func(string, bool) tea.Cmd { return nil },
	})
	handleManualResume(m)
	if m.nudgeFlash == "" {
		t.Error("N with no session under cursor should surface a flash")
	}
}
