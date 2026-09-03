package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/core"
)

// TestToggleQuotaGate_NoOptimisticFlip is the packet's own red-first test
// [design: Task 4.8 Step 1]: pressing P must never change the rendered
// gate state until a gateToggleResultMsg actually arrives. Simulating a
// "slow/never-arriving RPC" is exactly "the RPC's tea.Cmd is never
// invoked" -- if handleToggleQuotaGate flipped the gate synchronously
// (the optimistic-flip bug this test guards against), it would show up
// immediately, with no Cmd execution required at all.
func TestToggleQuotaGate_NoOptimisticFlip(t *testing.T) {
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			return "paused", nil
		},
	})
	m.reply = StatusReply{Gates: []Gate{{Name: core.GateQuotaPaused, Set: false}}}

	cmd := m.handleToggleQuotaGate()
	if cmd == nil {
		t.Fatal("handleToggleQuotaGate returned a nil cmd")
	}
	if !m.gateTogglePending {
		t.Error("gateTogglePending = false right after P, want true (pending indicator)")
	}
	if m.gateSet(core.GateQuotaPaused) {
		t.Fatal("quota_paused flipped to true before any gateToggleResultMsg arrived -- optimistic flip")
	}

	// The RPC "never arrives" in this branch of the test: cmd() is simply
	// never invoked. Nothing changes the pre-toggle state on its own.
	if m.gateSet(core.GateQuotaPaused) {
		t.Fatal("quota_paused state changed with no gateToggleResultMsg delivered")
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
	if !mm.gateSet(core.GateQuotaPaused) {
		t.Error("quota_paused still clear after a successful \"paused\" result")
	}
	if mm.gateTogglePending {
		t.Error("gateTogglePending still true after the result arrived")
	}
}

// TestToggleQuotaGate_UsesResumeWhenAlreadyPaused: P is a TOGGLE, not
// always-pause -- when quota_paused is already set, pressing it must send
// core.SubcommandResume, not another pause.
func TestToggleQuotaGate_UsesResumeWhenAlreadyPaused(t *testing.T) {
	var gotVerb string
	m := newTestModel(&stubPoller{
		toggle: func(_ context.Context, verb string) (string, error) {
			gotVerb = verb
			return "resumed", nil
		},
	})
	m.reply = StatusReply{Gates: []Gate{{Name: core.GateQuotaPaused, Set: true}}}

	cmd := m.handleToggleQuotaGate()
	if cmd == nil {
		t.Fatal("handleToggleQuotaGate returned a nil cmd")
	}
	_ = cmd()
	if gotVerb != core.SubcommandResume {
		t.Errorf("ToggleGate called with verb %q, want %q (gate already paused)", gotVerb, core.SubcommandResume)
	}
}

// TestToggleQuotaGate_HandleNeverReachesRawClient documents Acceptance
// Criterion 2 structurally: handleToggleQuotaGate is defined entirely in
// terms of m.poller.ToggleGate (via startGateToggle) -- there is no
// *core.Client field on Model at all for it to reach for instead. This
// test exercises that path end to end so a future change reintroducing a
// raw client call would have to touch (and be caught changing) exactly
// this flow.
func TestToggleQuotaGate_HandleNeverReachesRawClient(t *testing.T) {
	called := false
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			called = true
			return "paused", nil
		},
	})
	cmd := m.handleToggleQuotaGate()
	_ = cmd()
	if !called {
		t.Fatal("handleToggleQuotaGate's cmd never invoked Poller.ToggleGate")
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
	m.reply = StatusReply{Gates: []Gate{{Name: core.GateQuotaPaused, Set: false}}}

	cmd := m.handleToggleQuotaGate()
	msg := cmd()
	res := msg.(gateToggleResultMsg)

	updated, flashCmd := m.Update(res)
	mm := updated.(*Model)

	if mm.gateTogglePending {
		t.Error("gateTogglePending still true after a failed toggle")
	}
	if mm.gateSet(core.GateQuotaPaused) {
		t.Error("quota_paused changed after a FAILED toggle -- state must stay put")
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
// worked example: clearing quota_paused while cicd_down remains set must
// flash that the pool is STILL paused, not imply it resumed.
func TestGateToggle_SuccessFlashNamesEffectiveAggregate(t *testing.T) {
	m := newTestModel(&stubPoller{
		toggle: func(context.Context, string) (string, error) {
			return "resumed", nil
		},
	})
	m.reply = StatusReply{Gates: []Gate{
		{Name: core.GateQuotaPaused, Set: true},
		{Name: core.GateCICDDown, Set: true},
	}}

	cmd := m.handleToggleQuotaGate()
	msg := cmd().(gateToggleResultMsg)
	updated, _ := m.Update(msg)
	mm := updated.(*Model)

	if mm.gateSet(core.GateQuotaPaused) {
		t.Error("quota_paused should be clear after a \"resumed\" result")
	}
	if !strings.Contains(mm.flash, "cicd-down") {
		t.Errorf("flash %q should name cicd-down as the reason the pool is STILL paused", mm.flash)
	}
	if mm.flashLevel != FlashInfo {
		t.Errorf("a successful toggle's flash level = %v, want FlashInfo", mm.flashLevel)
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

// TestResumeAllGates_ResumesInsideGatesModal is the positive half of the
// above: with the Gates modal open, R fires the resume RPC.
func TestResumeAllGates_ResumesInsideGatesModal(t *testing.T) {
	var gotVerb string
	m := newTestModel(&stubPoller{
		toggle: func(_ context.Context, verb string) (string, error) {
			gotVerb = verb
			return "resumed", nil
		},
	})
	m.activeModal = ModalGates

	cmd := handleResumeAllGates(m)
	if cmd == nil {
		t.Fatal("handleResumeAllGates returned a nil cmd with the Gates modal open")
	}
	_ = cmd()
	if gotVerb != core.SubcommandResume {
		t.Errorf("ToggleGate verb = %q, want %q", gotVerb, core.SubcommandResume)
	}
}

// TestRenderGatesModal_ListsBothGatesByName: the gates modal must name
// BOTH of INV-LIFE-2's two OR-effective gates, by their ADR-0026-safe
// hyphenated display names, with state/since/owner -- even one never
// observed by the core.
func TestRenderGatesModal_ListsBothGatesByName(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.reply = StatusReply{Gates: []Gate{
		{Name: core.GateQuotaPaused, Set: true, Mtime: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Owner: "operator"},
		// cicd_down deliberately absent -- never observed yet.
	}}
	m.activeModal = ModalGates

	got := m.renderGatesModal()
	for _, want := range []string{"quota-paused", "cicd-down", "SET", "clear", "operator", "resume all"} {
		if !strings.Contains(got, want) {
			t.Errorf("gates modal missing %q; got:\n%s", want, got)
		}
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
	m.reply = StatusReply{Gates: []Gate{{Name: core.GateQuotaPaused, Set: false}}}

	toggleStart := time.Now()
	m.gateToggleStartedAt = toggleStart
	m.gateTogglePending = true

	// Captured BEFORE the toggle started: discarded, even though it
	// disagrees with the (still pending) local state.
	stale := StatusReply{
		AsOf:  toggleStart.Add(-time.Second),
		Core:  CoreInfo{State: coreStateStarted},
		Gates: []Gate{{Name: core.GateQuotaPaused, Set: true}},
	}
	updated, _ := m.Update(pollResultMsg{reply: stale})
	mm := updated.(*Model)
	if mm.gateSet(core.GateQuotaPaused) {
		t.Fatal("a stale (pre-toggle) poll result overwrote the pending gate state")
	}
	if mm.screen != screenMain {
		t.Fatalf("screen changed on a discarded poll result: %v", mm.screen)
	}

	// The toggle itself settles.
	updated, _ = mm.Update(gateToggleResultMsg{effective: "resumed"})
	mm = updated.(*Model)
	if mm.gateSet(core.GateQuotaPaused) {
		t.Fatal("gate still set after a successful resume result")
	}

	// A FRESH poll (AsOf after the toggle start) applies normally, even
	// though it disagrees -- this is "the next poll catches up".
	fresh := StatusReply{
		AsOf:  toggleStart.Add(time.Second),
		Core:  CoreInfo{State: coreStateStarted},
		Gates: []Gate{{Name: core.GateQuotaPaused, Set: true}},
	}
	updated, _ = mm.Update(pollResultMsg{reply: fresh})
	mm = updated.(*Model)
	if !mm.gateSet(core.GateQuotaPaused) {
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
