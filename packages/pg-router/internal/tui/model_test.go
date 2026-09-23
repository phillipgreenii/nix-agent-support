package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// stubPoller is a hand-rolled Poller test double: each method just returns
// whatever the test wired up, with no real Discover/Dial/Call cycle (that
// machinery is poller.go's own, already covered by poller_test.go).
type stubPoller struct {
	snapshot func(ctx context.Context, since uint64) (StatusReply, error)
	toggle   func(ctx context.Context, verb string) (string, error)
}

func (s *stubPoller) Snapshot(ctx context.Context, since uint64) (StatusReply, error) {
	if s.snapshot == nil {
		return StatusReply{}, errors.New("stubPoller: Snapshot not wired")
	}
	return s.snapshot(ctx, since)
}

func (s *stubPoller) ToggleGate(ctx context.Context, verb string) (string, error) {
	if s.toggle == nil {
		return "", errors.New("stubPoller: ToggleGate not wired")
	}
	return s.toggle(ctx, verb)
}

// var _ tea.Model = (*Model)(nil) is a compile-time assertion that Model
// implements the tea.Model interface (Acceptance Criterion 1).
var _ tea.Model = (*Model)(nil)

func newTestModel(p Poller) *Model {
	return NewModel(Options{Poller: p}, render.NewTheme(false))
}

// TestLifecycleTable_NonStartedStateRendersQuiescingNotError is the packet's
// own red-first test [design: Task 4.5 Step 1]: a pollResultMsg carrying
// StatusReply{Core: CoreInfo{State: "draining"}} (any non-"started" value)
// transitions the Model to screenQuiescing -- never screenMain with a poll-
// error zone flagged. "draining" is not itself an error; it is a normal
// lifecycle phase (INV-LIFE-2).
func TestLifecycleTable_NonStartedStateRendersQuiescingNotError(t *testing.T) {
	m := newTestModel(nil)

	updated, cmd := m.Update(pollResultMsg{reply: StatusReply{Core: CoreInfo{State: "draining"}}})
	if cmd != nil {
		t.Errorf("Update on pollResultMsg returned a non-nil cmd, want nil")
	}
	mm, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if mm.screen != screenQuiescing {
		t.Fatalf("screen = %v, want %v (screenQuiescing)", mm.screen, screenQuiescing)
	}
	if mm.pollErrFlagged {
		t.Error("pollErrFlagged = true on a successful (non-started) reply, want false")
	}
	if mm.lastErr != nil {
		t.Errorf("lastErr = %v, want nil after a successful poll", mm.lastErr)
	}
}

// TestApplyPollResult_StartedStateEntersMain covers the table's "main" row:
// a successful reply with core.state == "started" is the default main
// screen, not quiescing.
func TestApplyPollResult_StartedStateEntersMain(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(pollResultMsg{reply: StatusReply{Core: CoreInfo{State: coreStateStarted}}})
	mm := updated.(*Model)
	if mm.screen != screenMain {
		t.Fatalf("screen = %v, want %v (screenMain)", mm.screen, screenMain)
	}
}

// TestApplyPollErr_NoRunningCoreEntersNoCoreScreen covers the table's
// "no-core" row: a pollErrMsg wrapping core.ErrNoRunningCore moves to
// screenNoCore regardless of the prior screen.
func TestApplyPollErr_NoRunningCoreEntersNoCoreScreen(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain // prior screen must not matter

	wrapped := fmt.Errorf("tui: poll: %w", core.ErrNoRunningCore)
	updated, _ := m.Update(pollErrMsg{err: wrapped})
	mm := updated.(*Model)

	if mm.screen != screenNoCore {
		t.Fatalf("screen = %v, want %v (screenNoCore)", mm.screen, screenNoCore)
	}
	if mm.pollErrFlagged {
		t.Error("pollErrFlagged = true on ErrNoRunningCore, want false (no-core has its own screen)")
	}
	if !errors.Is(mm.lastErr, core.ErrNoRunningCore) {
		t.Errorf("lastErr = %v, want it to wrap core.ErrNoRunningCore", mm.lastErr)
	}
}

// TestApplyPollErr_OtherFailureStaysOnCurrentScreenAndFlags is Binding
// Decision Step 3, literally: any poll failure that does NOT wrap
// core.ErrNoRunningCore leaves the screen exactly where it was and only
// flags the poll-error zone (rendered by the sibling packet covering Task
// 4.6).
func TestApplyPollErr_OtherFailureStaysOnCurrentScreenAndFlags(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain

	updated, _ := m.Update(pollErrMsg{err: errors.New("tui: poll: some other failure")})
	mm := updated.(*Model)

	if mm.screen != screenMain {
		t.Fatalf("screen = %v, want %v (unchanged)", mm.screen, screenMain)
	}
	if !mm.pollErrFlagged {
		t.Error("pollErrFlagged = false, want true on a non-ErrNoRunningCore failure")
	}
}

// TestApplyPollResult_ClearsAPriorPollErrFlag: a subsequent successful poll
// must not leave a stale poll-error flag latched (mirrors pa-monitor's own
// pollResultMsg handling, update.go's lastErr-clearing comment).
func TestApplyPollResult_ClearsAPriorPollErrFlag(t *testing.T) {
	m := newTestModel(nil)
	m.pollErrFlagged = true
	m.lastErr = errors.New("stale")

	updated, _ := m.Update(pollResultMsg{reply: StatusReply{Core: CoreInfo{State: coreStateStarted}}})
	mm := updated.(*Model)

	if mm.pollErrFlagged {
		t.Error("pollErrFlagged still true after a successful poll")
	}
	if mm.lastErr != nil {
		t.Errorf("lastErr = %v, want nil after a successful poll", mm.lastErr)
	}
}

// TestInit_NilPollerOrZeroIntervalReturnsNoCmd: Init must not schedule
// polling with nothing to poll or no cadence to poll on -- otherwise
// pollNow would panic dereferencing a nil Poller.
func TestInit_NilPollerOrZeroIntervalReturnsNoCmd(t *testing.T) {
	if cmd := newTestModel(nil).Init(); cmd != nil {
		t.Error("Init with a nil Poller returned a non-nil cmd")
	}
	m := NewModel(Options{Poller: &stubPoller{}, PollInterval: 0}, render.NewTheme(false))
	if cmd := m.Init(); cmd != nil {
		t.Error("Init with PollInterval 0 returned a non-nil cmd")
	}
}

// TestInit_PollerAndIntervalSchedulesPolling: with both set, Init returns a
// (non-nil) batch of the immediate poll plus the recurring tick, and flags
// polling so a pollTickMsg arriving before that first reply lands does not
// fire a second, overlapping poll.
func TestInit_PollerAndIntervalSchedulesPolling(t *testing.T) {
	m := NewModel(Options{Poller: &stubPoller{}, PollInterval: time.Second}, render.NewTheme(false))
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init with a Poller and a positive interval returned a nil cmd")
	}
	if !m.polling {
		t.Error("Init did not flag polling")
	}
}

// TestUpdate_PollTickWhileAlreadyPollingOnlyReschedules: a tick landing
// while a poll is still in flight must not fire a second, overlapping
// pollNow -- it only reschedules the next tick.
func TestUpdate_PollTickWhileAlreadyPollingOnlyReschedules(t *testing.T) {
	calls := 0
	m := NewModel(Options{
		Poller: &stubPoller{snapshot: func(context.Context, uint64) (StatusReply, error) {
			calls++
			return StatusReply{}, nil
		}},
		PollInterval: time.Second,
	}, render.NewTheme(false))
	m.polling = true

	_, cmd := m.Update(pollTickMsg{})
	if cmd == nil {
		t.Fatal("pollTickMsg while polling returned a nil cmd (want the rescheduled tick)")
	}
	// Executing the returned cmd must be the tick alone, never a second
	// pollNow -- run it and confirm it only ever produces pollTickMsg.
	msg := cmd()
	if _, ok := msg.(pollTickMsg); !ok {
		t.Fatalf("cmd() = %T, want pollTickMsg (no overlapping poll while one is in flight)", msg)
	}
	if calls != 0 {
		t.Errorf("Snapshot called %d times from a tick received while already polling, want 0", calls)
	}
}

// TestUpdate_WindowSizeMsgSetsWidthHeight: the only framework message this
// packet's Update reacts to besides the poll cycle.
func TestUpdate_WindowSizeMsgSetsWidthHeight(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm := updated.(*Model)
	if mm.width != 80 || mm.height != 24 {
		t.Fatalf("width,height = %d,%d; want 80,24", mm.width, mm.height)
	}
}

// TestPollNow_CarriesForwardTheSinceCursor: Snapshot's `since` argument must
// be whatever advanceSinceCursor last adopted -- the previous successful
// reply's highest Activity Seq -- never a fixed 0, or the ring would
// re-deliver everything on every poll (Task 4.4 Interfaces).
func TestPollNow_CarriesForwardTheSinceCursor(t *testing.T) {
	var gotSince uint64
	m := NewModel(Options{
		Poller: &stubPoller{snapshot: func(_ context.Context, since uint64) (StatusReply, error) {
			gotSince = since
			return StatusReply{}, nil
		}},
	}, render.NewTheme(false))
	m.sinceCursor = 42

	msg := m.pollNow()()
	if _, ok := msg.(pollResultMsg); !ok {
		t.Fatalf("pollNow() produced %T, want pollResultMsg", msg)
	}
	if gotSince != 42 {
		t.Errorf("Snapshot called with since=%d, want 42", gotSince)
	}
}

// TestApplyPollResult_AdvancesSinceCursorFromLastActivitySeq: the ring's own
// Read order is oldest-first, so the LAST entry in a reply's Activity slice
// carries the highest Seq -- that is what the next poll's `since` must
// become.
func TestApplyPollResult_AdvancesSinceCursorFromLastActivitySeq(t *testing.T) {
	m := newTestModel(nil)
	reply := StatusReply{
		Core: CoreInfo{State: coreStateStarted},
		Activity: []ActivityEntry{
			{Seq: 3},
			{Seq: 7},
		},
	}
	updated, _ := m.Update(pollResultMsg{reply: reply})
	mm := updated.(*Model)
	if mm.sinceCursor != 7 {
		t.Fatalf("sinceCursor = %d, want 7 (the last/highest Activity Seq)", mm.sinceCursor)
	}
}

// TestView_ScreenLoadingIsLiteralRegardlessOfWidth: Binding Decision (Step
// 4) -- screenLoading's View is the literal "loading…" string no matter
// what width is, reusing pa-monitor's own width=0 contract for the pre-
// first-poll case.
func TestView_ScreenLoadingIsLiteralRegardlessOfWidth(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 40 // a real width -- must still render "loading…"
	if got := m.View(); got != "loading…" {
		t.Fatalf("View() = %q, want the literal %q", got, "loading…")
	}
}

// TestView_ZeroWidthAlwaysRendersLoading mirrors pa-monitor's own pre-first-
// WindowSizeMsg guard: width == 0 renders "loading…" regardless of screen.
func TestView_ZeroWidthAlwaysRendersLoading(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	if got := m.View(); got != "loading…" {
		t.Fatalf("View() with width==0 = %q, want %q", got, "loading…")
	}
}

// TestView_NoCoreScreenRendersNoCoreMessage confirms the no-core screen
// routes through noCoreMessage (content is nocore_test.go's own concern via
// screen_test.go), and -- pg2-3ll1n -- that the routed call is given
// m.height so the rendered output fills the terminal fullscreen the same
// way every other screen already does (screen_test.go's own
// TestNoCoreMessage_FillsRequestedHeight covers noCoreMessage's height-fill
// contract directly; this test is the end-to-end wiring check that View()
// actually forwards m.height rather than dropping it).
func TestView_NoCoreScreenRendersNoCoreMessage(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.screen = screenNoCore
	m.lastErr = fmt.Errorf("tui: poll: %w", core.ErrNoRunningCore)
	got := m.View()
	if !strings.Contains(got, "No core running") {
		t.Fatalf("View() on screenNoCore = %q, want it to route through noCoreMessage", got)
	}
	if gotLines := strings.Split(got, "\n"); len(gotLines) != m.height {
		t.Errorf("View() on screenNoCore returned %d lines, want exactly m.height=%d (fullscreen, like every other screen); got:\n%s", len(gotLines), m.height, got)
	}
}

// TestRun_NonTerminalOutputFailsFastOnThemeDetection: Run must resolve the
// render theme against the real output BEFORE ever starting a bubbletea
// program -- a *bytes.Buffer is never a terminal (colorprofile.NoTTY), so
// Run must fail exactly the way render.Detect does, rather than starting a
// program a test (or a piped/redirected production invocation) could never
// drive to completion.
func TestRun_NonTerminalOutputFailsFastOnThemeDetection(t *testing.T) {
	var buf bytes.Buffer
	err := Run(Options{Out: &buf})
	if err == nil {
		t.Fatal("Run with a non-terminal Out returned nil, want render.Detect's NoTTY error")
	}
	if !strings.Contains(err.Error(), "NoTTY") {
		t.Errorf("Run error = %v, want it to name the NoTTY profile (render.Detect's own error)", err)
	}
}

// lineContaining returns the index of the first line of s containing sub,
// or -1 if no line does [design: Task 4: Two-tier layout (static above
// dynamic), Binding decisions].
func lineContaining(s, sub string) int {
	for i, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return i
		}
	}
	return -1
}

// TestRenderMain_StaticTierPositionUnaffectedByActivitySize is this
// packet's own red-first test [design: Task 4: Two-tier layout (static
// above dynamic), Step 1]: the static tier (Listeners, Sources) sits above
// the dynamic tier (Queues, Activity) in renderMain's zone list, so
// growing the Activity pane's content never moves the Listeners line.
// Focus is pinned to Sources (rather than left at the zero-value
// paneListeners) so Listeners renders as an ordinary dropOrder-based zone
// with real content, not the one FILL zone whose content a headless
// (m.height == 0) render would otherwise omit entirely.
func TestRenderMain_StaticTierPositionUnaffectedByActivitySize(t *testing.T) {
	base := func(activityEntries int) *Model {
		m := newTestModel(nil)
		m.focusedPane = paneSources
		activity := make([]ActivityEntry, activityEntries)
		for i := range activity {
			activity[i] = ActivityEntry{Seq: uint64(i), Type: "x", Outcome: "delivered"}
		}
		m.reply = StatusReply{
			Listeners: []Listener{{Role: "df-feedback"}},
			Sources:   []Source{{Name: "pr-sweep"}},
			Activity:  activity,
		}
		return m
	}

	small := base(1).renderMain()
	large := base(20).renderMain()

	listenersLineSmall := lineContaining(small, "Listeners")
	listenersLineLarge := lineContaining(large, "Listeners")
	if listenersLineSmall < 0 {
		t.Fatalf("Listeners pane not found in small-activity render:\n%s", small)
	}
	if listenersLineSmall != listenersLineLarge {
		t.Fatalf("Listeners pane moved from line %d to %d as Activity grew", listenersLineSmall, listenersLineLarge)
	}
}

// TestUnfocusedPaneDropOrder_StaticTierNeverDropsBeforeDynamicTier is a
// direct unit test on the pure ordering function, rather than driving
// layoutZones through an actual height-constrained render -- Review Focus:
// under height pressure, Activity and Queues must be fully gone before
// Listeners or Sources drops even one row [design: Task 4: Two-tier layout
// (static above dynamic), Step 1].
func TestUnfocusedPaneDropOrder_StaticTierNeverDropsBeforeDynamicTier(t *testing.T) {
	if unfocusedPaneDropOrder(paneActivity) >= unfocusedPaneDropOrder(paneQueues) {
		t.Fatalf("Activity (%d) must drop before Queues (%d)", unfocusedPaneDropOrder(paneActivity), unfocusedPaneDropOrder(paneQueues))
	}
	if unfocusedPaneDropOrder(paneQueues) >= unfocusedPaneDropOrder(paneListeners) {
		t.Fatalf("Queues (%d) must drop before Listeners (%d)", unfocusedPaneDropOrder(paneQueues), unfocusedPaneDropOrder(paneListeners))
	}
	if unfocusedPaneDropOrder(paneQueues) >= unfocusedPaneDropOrder(paneSources) {
		t.Fatalf("Queues (%d) must drop before Sources (%d)", unfocusedPaneDropOrder(paneQueues), unfocusedPaneDropOrder(paneSources))
	}
}

// TestRenderMain_ExtremeHeightPressureDropsDynamicTierBeforeStatic drives
// the REAL layoutZones algorithm through renderMain (not just the pure
// ordering function above) at a deliberately short m.height, closing the
// gap an earlier plan review found: the pure-function test alone cannot
// catch a bug in layoutZones' own height-budget logic [design: Task 4:
// Two-tier layout (static above dynamic), Step 1; Review Focus].
func TestRenderMain_ExtremeHeightPressureDropsDynamicTierBeforeStatic(t *testing.T) {
	m := newTestModel(nil)
	m.reply = StatusReply{
		Listeners: []Listener{{Role: "df-feedback"}},
		Sources:   []Source{{Name: "pr-sweep"}},
		Queues:    []Queue{{Type: "pr.changed", Depth: 1}},
		Activity:  []ActivityEntry{{Seq: 1, Type: "x"}},
	}
	m.height = 6 // deliberately too short for every zone to fit

	out := m.renderMain()

	if !strings.Contains(out, "Listeners") {
		t.Fatalf("static tier (Listeners) must survive extreme height pressure, got:\n%s", out)
	}
	if strings.Contains(out, "Activity") && strings.Contains(out, "Queues") {
		t.Fatalf("expected at least one dynamic-tier pane already dropped before the static tier would ever drop, got:\n%s", out)
	}
}
