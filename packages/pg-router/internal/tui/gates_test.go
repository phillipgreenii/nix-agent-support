package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
)

// TestToggleOperatorGate_NoOptimisticFlip is the packet's own red-first test
// [design: Task 4.8 Step 1]: pressing P must never change the rendered
// gate state until a gateToggleResultMsg actually arrives. Simulating a
// "slow/never-arriving RPC" is exactly "the RPC's tea.Cmd is never
// invoked" -- if handleToggleOperatorGate flipped the gate synchronously
// (the optimistic-flip bug this test guards against), it would show up
// immediately, with no Cmd execution required at all.
func TestToggleOperatorGate_NoOptimisticFlip(t *testing.T) {
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			return "paused", nil
		},
	})
	m.reply = StatusReply{}

	cmd := m.handleToggleOperatorGate()
	if cmd == nil {
		t.Fatal("handleToggleOperatorGate returned a nil cmd")
	}
	if !m.gateTogglePending {
		t.Error("gateTogglePending = false right after P, want true (pending indicator)")
	}
	if m.gateSet(core.GateSystemPause) {
		t.Fatal("SYSTEM_PAUSE flipped to set before any gateToggleResultMsg arrived -- optimistic flip")
	}

	// Now the reply DOES arrive -- Update's gateToggleResultMsg case is the
	// only path allowed to change the rendered state.
	msg := cmd()
	res, ok := msg.(gateToggleResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want gateToggleResultMsg", msg)
	}
	updated, _ := m.Update(res)
	mm := updated.(*Model)
	if !mm.gateSet(core.GateSystemPause) {
		t.Error("SYSTEM_PAUSE still clear after a successful \"paused\" result")
	}
	if mm.gateTogglePending {
		t.Error("gateTogglePending still true after the result arrived")
	}
}

// TestToggleOperatorGate_UsesResumeWhenAlreadyPaused: P is a TOGGLE, not
// always-pause -- when SYSTEM_PAUSE is already active, pressing it must send
// core.SubcommandResume, not another pause.
func TestToggleOperatorGate_UsesResumeWhenAlreadyPaused(t *testing.T) {
	var gotVerb string
	m := newTestModel(&stubPoller{
		toggle: func(_ context.Context, verb string) (string, error) {
			gotVerb = verb
			return "resumed", nil
		},
	})
	m.reply = StatusReply{Gates: []Gate{{Type: core.GateSystemPause}}}

	cmd := m.handleToggleOperatorGate()
	if cmd == nil {
		t.Fatal("handleToggleOperatorGate returned a nil cmd")
	}
	_ = cmd()
	if gotVerb != core.SubcommandResume {
		t.Errorf("ToggleGate called with verb %q, want %q (gate already paused)", gotVerb, core.SubcommandResume)
	}
}

// TestToggleOperatorGate_IgnoresOtherGates: P toggles SYSTEM_PAUSE only. A
// different gate being active must not make P send "resume" (which would
// merely be a no-op for it): the operator still wants to PAUSE.
func TestToggleOperatorGate_IgnoresOtherGates(t *testing.T) {
	var gotVerb string
	m := newTestModel(&stubPoller{
		toggle: func(_ context.Context, verb string) (string, error) {
			gotVerb = verb
			return "paused", nil
		},
	})
	m.reply = StatusReply{Gates: []Gate{{Type: "LOW_DISK_USAGE"}}}
	_ = m.handleToggleOperatorGate()()
	if gotVerb != core.SubcommandPause {
		t.Errorf("verb = %q, want pause: another system's gate does not make P a resume", gotVerb)
	}
}

// TestToggleOperatorGate_HandleNeverReachesRawClient documents Acceptance
// Criterion 2 structurally: handleToggleOperatorGate is defined entirely in
// terms of m.poller.ToggleGate (via startGateToggle) -- there is no
// *core.Client field on Model at all for it to reach for instead.
func TestToggleOperatorGate_HandleNeverReachesRawClient(t *testing.T) {
	called := false
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			called = true
			return "paused", nil
		},
	})
	cmd := m.handleToggleOperatorGate()
	_ = cmd()
	if !called {
		t.Fatal("handleToggleOperatorGate's cmd never invoked Poller.ToggleGate")
	}
}

// TestGateToggle_FailureFlash is Binding Decision Step 3: an RPC error
// clears the pending indicator and produces a warn-level flash naming the
// failure, leaving the rendered gate state untouched.
func TestGateToggle_FailureFlash(t *testing.T) {
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			return "", errors.New("dial: no running core")
		},
	})
	m.reply = StatusReply{}

	cmd := m.handleToggleOperatorGate()
	msg := cmd()
	res := msg.(gateToggleResultMsg)

	updated, flashCmd := m.Update(res)
	mm := updated.(*Model)

	if mm.gateTogglePending {
		t.Error("gateTogglePending still true after a failed toggle")
	}
	if mm.gateSet(core.GateSystemPause) {
		t.Error("SYSTEM_PAUSE changed after a FAILED toggle -- state must stay put")
	}
	if mm.flash == "" || mm.flashLevel != FlashWarn {
		t.Errorf("flash = %q level=%v, want a non-empty FlashWarn flash naming the failure", mm.flash, mm.flashLevel)
	}
	if !strings.Contains(mm.flash, "no running core") {
		t.Errorf("flash %q should name the underlying failure", mm.flash)
	}
	if flashCmd == nil {
		t.Fatal("Update on a failed gateToggleResultMsg returned a nil cmd, want the flash-clear tick")
	}
}

// TestGateToggle_SuccessFlashNamesEffectiveAggregate is the design's own
// worked example: clearing SYSTEM_PAUSE while another gate remains active
// must flash that the pool is STILL gated, not imply it resumed.
func TestGateToggle_SuccessFlashNamesEffectiveAggregate(t *testing.T) {
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			return "resumed", nil
		},
	})
	m.reply = StatusReply{Gates: []Gate{
		{Type: core.GateSystemPause},
		{Type: "LOW_DISK_USAGE"},
	}}

	cmd := m.handleToggleOperatorGate()
	msg := cmd().(gateToggleResultMsg)
	updated, _ := m.Update(msg)
	mm := updated.(*Model)

	if mm.gateSet(core.GateSystemPause) {
		t.Error("SYSTEM_PAUSE should be clear after a \"resumed\" result")
	}
	if !mm.gateSet("LOW_DISK_USAGE") {
		t.Error("the other system's gate must survive a SYSTEM_PAUSE resume")
	}
	if !strings.Contains(mm.flash, "LOW_DISK_USAGE") {
		t.Errorf("flash %q should name LOW_DISK_USAGE as the reason the pool is STILL gated", mm.flash)
	}
	if mm.flashLevel != FlashInfo {
		t.Errorf("a successful toggle's flash level = %v, want FlashInfo", mm.flashLevel)
	}
}

// TestGateToggle_ResumedFlashWhenNothingElseIsActive: the plain case.
func TestGateToggle_ResumedFlashWhenNothingElseIsActive(t *testing.T) {
	m := newTestModel(nil)
	m.reply = StatusReply{Gates: []Gate{{Type: core.GateSystemPause}}}
	m.applyGateToggleResult(gateToggleResultMsg{verb: core.SubcommandResume, effective: "resumed"})
	if !strings.Contains(m.flash, "RESUMED") {
		t.Errorf("flash %q, want the pool-resumed wording when no other gate is active", m.flash)
	}
}

// TestResumeAllGates_NoopOutsideGatesModal: R only means anything inside
// the open Gates modal (the design's own g-row text) -- everywhere else
// it must be a true no-op, never an accidental resume.
func TestResumeAllGates_NoopOutsideGatesModal(t *testing.T) {
	called := false
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			called = true
			return "resumed", nil
		},
	})
	m.activeModal = ModalNone
	if cmd := handleResumeAllGates(m); cmd != nil {
		t.Error("handleResumeAllGates returned a non-nil cmd with no modal open")
	}
	m.activeModal = ModalHelp
	if cmd := handleResumeAllGates(m); cmd != nil {
		t.Error("handleResumeAllGates returned a non-nil cmd with the HELP modal open, not Gates")
	}
	if called {
		t.Fatal("ToggleGate was invoked despite the Gates modal not being open")
	}
}

// TestResumeAllGates_ClearsEveryGateInsideGatesModal is the positive half of
// the above: with the Gates modal open, R fires the resume-all RPC and, on
// success, empties the rendered gate list (any caller may clear any gate).
func TestResumeAllGates_ClearsEveryGateInsideGatesModal(t *testing.T) {
	var gotVerb string
	m := newTestModel(&stubPoller{
		toggle: func(_ context.Context, verb string) (string, error) {
			gotVerb = verb
			return "resumed", nil
		},
	})
	m.activeModal = ModalGates
	m.reply = StatusReply{Gates: []Gate{{Type: core.GateSystemPause}, {Type: "LOW_DISK_USAGE"}}}

	cmd := handleResumeAllGates(m)
	if cmd == nil {
		t.Fatal("handleResumeAllGates returned a nil cmd with the Gates modal open")
	}
	res := cmd().(gateToggleResultMsg)
	if gotVerb != ToggleVerbResumeAll {
		t.Errorf("ToggleGate verb = %q, want %q", gotVerb, ToggleVerbResumeAll)
	}
	m.Update(res)
	if len(m.reply.Gates) != 0 {
		t.Errorf("gates after resume-all = %+v, want none", m.reply.Gates)
	}
}

// TestRenderGatesModal_ListsEveryActiveGate: the modal shows TYPE,
// description, owner, set-at and TTL remaining for each gate in force,
// whatever the (arbitrary) TYPE is.
func TestRenderGatesModal_ListsEveryActiveGate(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 30
	setAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	m.reply = StatusReply{Gates: []Gate{
		{Type: "LOW_DISK_USAGE", Description: "free space 3GiB", Owner: "disk-watchdog", SetAt: setAt, ExpiresAt: setAt.Add(5 * time.Minute), TTLRemainingMs: 270000},
		{Type: core.GateSystemPause, Description: "paused by the operator", Owner: "operator", SetAt: setAt},
	}}
	got := m.renderGatesModal()
	for _, want := range []string{
		"SYSTEM_PAUSE", "LOW_DISK_USAGE",
		"disk-watchdog", "operator", "2026-09-01 12:00", "no TTL", "TTL 4m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Gates modal missing %q; got:\n%s", want, got)
		}
	}
	if strings.Index(got, "LOW_DISK_USAGE") > strings.Index(got, "SYSTEM_PAUSE") {
		t.Errorf("gates are not sorted by TYPE; got:\n%s", got)
	}
	if !strings.Contains(got, "[R] resume all") {
		t.Errorf("Gates modal footer must name R = resume all; got:\n%s", got)
	}
}

// TestRenderGatesModal_NoneActiveIsUnambiguous (pg2-y6sy5's spirit): with
// nothing gated the modal says so outright rather than rendering nothing.
func TestRenderGatesModal_NoneActiveIsUnambiguous(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.reply = StatusReply{}
	got := m.renderGatesModal()
	if !strings.Contains(got, "none active") {
		t.Errorf("empty Gates modal must read \"none active\"; got:\n%s", got)
	}
}

// TestRenderGatesModal_LongTypeGetsGuaranteedGap is pg2-y6sy5's regression
// test, carried forward: a long gate TYPE must never run straight into the
// detail text with no gap.
func TestRenderGatesModal_LongTypeGetsGuaranteedGap(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 24
	long := "A_VERY_LONG_ARBITRARY_GATE_TYPE_NAME"
	m.reply = StatusReply{Gates: []Gate{{Type: long, Description: "d", Owner: "o"}}}
	got := m.renderGatesModal()
	if strings.Contains(got, long+"d") {
		t.Errorf("gate TYPE runs into its detail with no gap; got:\n%s", got)
	}
	if !strings.Contains(got, long) {
		t.Errorf("gate TYPE missing; got:\n%s", got)
	}
}

// TestAsOfRaceGuard is Binding Decision Step 5: a poll result captured
// before an in-flight/just-settled toggle started must be discarded
// outright, never overwriting the pending/just-toggled gate state; a
// poll captured AFTER the toggle started applies normally, even when it
// disagrees.
func TestAsOfRaceGuard(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.reply = StatusReply{}

	toggleStart := time.Now()
	m.gateToggleStartedAt = toggleStart
	m.gateTogglePending = true

	// Captured BEFORE the toggle started: discarded, even though it
	// disagrees with the (still pending) local state.
	stale := StatusReply{
		AsOf:  toggleStart.Add(-time.Second),
		Core:  CoreInfo{State: coreStateStarted},
		Gates: []Gate{{Type: core.GateSystemPause}},
	}
	updated, _ := m.Update(pollResultMsg{reply: stale})
	mm := updated.(*Model)
	if mm.gateSet(core.GateSystemPause) {
		t.Fatal("a stale (pre-toggle) poll result overwrote the pending gate state")
	}
	if mm.screen != screenMain {
		t.Fatalf("screen changed on a discarded poll result: %v", mm.screen)
	}

	// The toggle itself settles.
	updated, _ = mm.Update(gateToggleResultMsg{verb: core.SubcommandResume, effective: "resumed"})
	mm = updated.(*Model)
	if mm.gateSet(core.GateSystemPause) {
		t.Fatal("gate still set after a successful resume result")
	}

	// A FRESH poll (AsOf after the toggle start) applies normally, even
	// though it disagrees -- this is "the next poll catches up".
	fresh := StatusReply{
		AsOf:  toggleStart.Add(time.Second),
		Core:  CoreInfo{State: coreStateStarted},
		Gates: []Gate{{Type: core.GateSystemPause}},
	}
	updated, _ = mm.Update(pollResultMsg{reply: fresh})
	mm = updated.(*Model)
	if !mm.gateSet(core.GateSystemPause) {
		t.Fatal("a fresh (post-toggle) poll result was not applied")
	}
	if mm.screen != screenMain {
		t.Fatalf("a fresh poll result should still resolve the normal screen transition: %v", mm.screen)
	}
}

// TestApplyPollResult_PreservesOpenModalScreen: a poll landing while a
// modal is open must refresh the underlying data without yanking the
// screen back to main/quiescing out from under the operator.
func TestApplyPollResult_PreservesOpenModalScreen(t *testing.T) {
	m := newTestModel(nil)
	m.openModal(ModalGates)

	updated, _ := m.Update(pollResultMsg{reply: StatusReply{Core: CoreInfo{State: coreStateStarted}}})
	mm := updated.(*Model)
	if mm.screen != screenModal {
		t.Fatalf("screen = %v after a poll while a modal was open, want screenModal preserved", mm.screen)
	}
	if mm.activeModal != ModalGates {
		t.Fatalf("activeModal = %v, want ModalGates preserved across the poll", mm.activeModal)
	}
}

// TestGateDetail_CarriesEveryField: the detail line carries TTL left, set-at,
// owner and description (the modal may clip the tail, so the unclipped text is
// checked here directly), with explicit placeholders for what a gate omits.
func TestGateDetail_CarriesEveryField(t *testing.T) {
	setAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	full := gateDetail(Gate{Type: "X", Description: "free space 3GiB", Owner: "disk-watchdog", SetAt: setAt, ExpiresAt: setAt.Add(5 * time.Minute), TTLRemainingMs: 270000})
	for _, want := range []string{"TTL 4m", "2026-09-01 12:00", "disk-watchdog", "free space 3GiB"} {
		if !strings.Contains(full, want) {
			t.Errorf("gateDetail missing %q: %q", want, full)
		}
	}
	bare := gateDetail(Gate{Type: "X"})
	for _, want := range []string{"no TTL", "owner: -", "(no description)"} {
		if !strings.Contains(bare, want) {
			t.Errorf("gateDetail of a bare gate missing %q: %q", want, bare)
		}
	}
}
