package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/reexec"
)

type tuiReexecCall struct {
	attempt int
	outcome string
}

// mismatchPoll builds a pollResultMsg carrying a daemon version, so driving it
// through Update exercises the version-mismatch self-restart decision.
func mismatchPoll(daemonVersion string) pollResultMsg {
	return pollResultMsg{tree: &aggregate.Tree{}, meta: DaemonMeta{DaemonVersion: daemonVersion}}
}

// yieldsQuit reports whether invoking cmd produces a tea.QuitMsg.
func yieldsQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func newReexecModel(t *testing.T, autoRestart bool, attemptBase int, calls *[]tuiReexecCall) *Model {
	t.Helper()
	return NewModel(Options{
		Tree:                         &aggregate.Tree{},
		Version:                      "OLD",
		AutoRestartOnVersionMismatch: autoRestart,
		ReexecAttemptBase:            attemptBase,
		RecordReexec:                 func(a int, o string) { *calls = append(*calls, tuiReexecCall{a, o}) },
	})
}

func TestTUIReexecRequestedOnMismatch(t *testing.T) {
	var calls []tuiReexecCall
	m := newReexecModel(t, true, 0, &calls)

	_, cmd := m.Update(mismatchPoll("NEW"))

	if !yieldsQuit(cmd) {
		t.Fatal("expected tea.Quit on mismatch with auto-restart on and budget remaining")
	}
	if !m.ReexecRequested() {
		t.Fatal("ReexecRequested() = false, want true")
	}
	if m.ReexecAttempt() != 0 {
		t.Errorf("ReexecAttempt() = %d, want 0", m.ReexecAttempt())
	}
	// The attempt metric belongs to runTUIRemote (just before the exec), not here.
	if len(calls) != 0 {
		t.Errorf("Update must not emit the attempt metric, got %v", calls)
	}
}

func TestTUINoReexecWhenDisabled(t *testing.T) {
	var calls []tuiReexecCall
	m := newReexecModel(t, false /* autoRestart off */, 0, &calls)

	_, cmd := m.Update(mismatchPoll("NEW"))

	if yieldsQuit(cmd) {
		t.Fatal("must not quit when auto-restart is disabled")
	}
	if m.ReexecRequested() {
		t.Fatal("ReexecRequested() = true, want false when disabled")
	}
}

func TestTUIReexecMatchResetsAttemptBase(t *testing.T) {
	var calls []tuiReexecCall
	m := newReexecModel(t, true, 2, &calls)

	_, cmd := m.Update(mismatchPoll("OLD")) // daemon == client "OLD": converged

	if yieldsQuit(cmd) {
		t.Fatal("matching version must not quit")
	}
	if m.ReexecAttempt() != 0 {
		t.Errorf("attempt base = %d, want reset to 0 on convergence", m.ReexecAttempt())
	}
}

func TestTUIReexecExhaustedGivesUp(t *testing.T) {
	var calls []tuiReexecCall
	m := newReexecModel(t, true, reexec.MaxAttempts, &calls)

	_, cmd := m.Update(mismatchPoll("NEW"))

	if yieldsQuit(cmd) {
		t.Fatal("exhausted budget must NOT quit (warn-only, stays running)")
	}
	if m.ReexecRequested() {
		t.Fatal("exhausted budget must not request reexec")
	}
	if !m.reexecGaveUp {
		t.Fatal("exhausted budget must set reexecGaveUp for the persistent alert")
	}
	if len(calls) != 1 || calls[0].outcome != otel.ReexecOutcomeExhausted || calls[0].attempt != reexec.MaxAttempts {
		t.Errorf("want one exhausted metric at attempt=%d, got %v", reexec.MaxAttempts, calls)
	}
}

// TestTUIReexecGaveUpStopsRequesting mirrors runTUIRemote's exec-failure path:
// after MarkReexecGaveUp the TUI must warn-only (no further quit) so it does not
// busy-loop re-exec ⇄ Run.
func TestTUIReexecGaveUpStopsRequesting(t *testing.T) {
	var calls []tuiReexecCall
	m := newReexecModel(t, true, 0, &calls)
	m.MarkReexecGaveUp()

	_, cmd := m.Update(mismatchPoll("NEW"))
	if yieldsQuit(cmd) {
		t.Fatal("must not re-request a quit once gave up")
	}
	if m.ReexecRequested() {
		t.Fatal("ReexecRequested() must stay false after MarkReexecGaveUp")
	}
	if len(calls) != 0 {
		t.Errorf("no new metric once gave up, got %v", calls)
	}
}

// TestTUIReexecDeferredWhileModalOpen asserts a disruptive quit is deferred
// while the user has a modal open, then fires once the modal closes.
func TestTUIReexecDeferredWhileModalOpen(t *testing.T) {
	var calls []tuiReexecCall
	m := newReexecModel(t, true, 0, &calls)
	m.SetActiveModalForTest(ModalHelp)

	_, cmd := m.Update(mismatchPoll("NEW"))
	if yieldsQuit(cmd) {
		t.Fatal("must defer the quit while a modal is open")
	}
	if m.ReexecRequested() {
		t.Fatal("must not request reexec while mid-interaction")
	}

	// Close the modal; the next poll should now trigger the deferred quit.
	m.SetActiveModalForTest(ModalNone)
	_, cmd = m.Update(mismatchPoll("NEW"))
	if !yieldsQuit(cmd) {
		t.Fatal("expected the deferred quit once the modal closed")
	}
	if !m.ReexecRequested() {
		t.Fatal("ReexecRequested() = false after modal closed, want true")
	}
}
