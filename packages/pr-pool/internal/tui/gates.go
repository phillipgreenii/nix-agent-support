// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.8) carries the command-pattern P gate toggle, its R = resume-all
// sub-binding, and the gates modal (g) that lists both of INV-LIFE-2's
// named gates.
package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// gateToggleTimeout bounds one ToggleGate round trip from the Model's
// side -- the same safety-net role pollTimeout plays above the Poller's
// own internal deadline (poll.go's pollTimeout above poller.go's
// pollerRPCDeadline). A core that never replies cannot pin the pending
// indicator forever: the RPC deadline is what clears it with a warn flash
// [design: Task 4.8 Step 3].
const gateToggleTimeout = 10 * time.Second

// gateToggleResultMsg carries ToggleGate's outcome back to Update
// [design: Task 4.8 Interfaces].
type gateToggleResultMsg struct {
	effective string
	err       error
}

// handleToggleOperatorGate implements the P key [design: Task 4.8 Files]: a
// command-pattern toggle of the operator_paused gate ONLY -- never
// cicd_down, which is automation-owned (see handleResumeAllGates's own
// doc for why the socket verb cannot reach it anyway). It calls
// m.poller.ToggleGate(ctx, verb), never a raw *core.Client, via
// startGateToggle.
//
// No optimistic flip, ever [design: Task 4.8 Step 1]: this method only
// resolves which verb to send and stamps the pending indicator: it never
// itself changes m.reply's gate state. That happens in exactly one place,
// applyGateToggleResult, and only once the RPC has actually replied.
func (m *Model) handleToggleOperatorGate() tea.Cmd {
	verb := core.SubcommandPause
	if m.gateSet(core.GateOperatorPaused) {
		verb = core.SubcommandResume
	}
	return m.startGateToggle(verb)
}

// handleResumeAllGates implements the "R = resume-all inside the Gates
// modal" sub-binding [design: Task 4.8 Files]: a no-op everywhere except
// while the Gates modal is open. "All" is bounded by what Poller.ToggleGate can actually
// reach (Task 4.4 Interfaces, internal/core/core.go's handleGateToggle):
// the socket resume verb's request carries no "gate" field at all, so it
// always targets the DEFAULT gate (operator_paused) -- cicd_down is
// automation-owned and has no operator-facing socket verb to clear it
// through in the first place. Resuming "all" the operator can affect and
// resuming the operator gate are therefore the same RPC today.
func handleResumeAllGates(m *Model) tea.Cmd {
	if m.activeModal != ModalGates {
		return nil
	}
	return m.startGateToggle(core.SubcommandResume)
}

// startGateToggle stamps the pending indicator and the asOf race-guard
// threshold (model.go's applyPollResult reads gateToggleStartedAt), then
// returns the tea.Cmd that performs verb's RPC via m.poller.ToggleGate,
// bounded by gateToggleTimeout. A nil Poller (a test driving Update by
// hand with no Poller wired) is a no-op: there is nothing to call.
func (m *Model) startGateToggle(verb string) tea.Cmd {
	if m.poller == nil {
		return nil
	}
	m.gateTogglePending = true
	m.gateToggleStartedAt = time.Now()
	poller := m.poller
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gateToggleTimeout)
		defer cancel()
		effective, err := poller.ToggleGate(ctx, verb)
		return gateToggleResultMsg{effective: effective, err: err}
	}
}

// applyGateToggleResult is Update's gateToggleResultMsg handler
// [design: Task 4.8 Step 3]. It always clears the pending indicator.
//
// On failure (an RPC error, including the gateToggleTimeout deadline
// firing with no reply): the rendered gate state is left exactly where it
// was, a warn flash names the failure, and the failure is logged.
//
// On success: this is the ONE place allowed to change the rendered gate
// state (the no-optimistic-flip contract's other half) -- it applies
// `effective` to the operator_paused gate locally so the operator sees the
// new state immediately rather than waiting for the next poll tick, and
// flashes the resulting EFFECTIVE aggregate, not just the toggled gate:
// clearing operator_paused while cicd_down remains set must not imply the
// pool resumed [design: Task 4.8 (worked flash example)].
func (m *Model) applyGateToggleResult(msg gateToggleResultMsg) tea.Cmd {
	m.gateTogglePending = false
	if msg.err != nil {
		m.errorLogger.LogString("gate toggle failed: " + msg.err.Error())
		m.setFlash("operator gate toggle failed: "+msg.err.Error(), FlashWarn)
		return m.flashClearCmd()
	}
	m.setGate(core.GateOperatorPaused, msg.effective == "paused")
	m.setFlash(m.operatorGateFlashText(msg.effective), FlashInfo)
	return m.flashClearCmd()
}

// operatorGateFlashText names the resulting EFFECTIVE aggregate, not just the
// toggled gate [design: Task 4.8 (worked flash example)]: clearing
// operator_paused while cicd_down remains set still leaves the pool paused
// overall (INV-LIFE-2's OR-effective semantics), and the flash says so
// rather than implying the pool resumed.
func (m *Model) operatorGateFlashText(effective string) string {
	if effective == "paused" {
		return "operator gate paused — pool now PAUSED"
	}
	if m.gateSet(core.GateCICDDown) {
		return "operator gate cleared — still PAUSED by cicd-down"
	}
	return "operator gate cleared — pool now RESUMED"
}

// gate looks up the named gate (core.GateOperatorPaused / core.GateCICDDown)
// in the last-polled reply, reporting whether it has ever actually been
// observed. A gate absent from m.reply.Gates (never yet observed by the
// core) reports the zero value, ok=false.
func (m *Model) gate(name string) (Gate, bool) {
	for _, g := range m.reply.Gates {
		if g.Name == name {
			return g, true
		}
	}
	return Gate{}, false
}

// gateSet reports whether the named gate is currently SET in the
// last-polled reply.
func (m *Model) gateSet(name string) bool {
	g, _ := m.gate(name)
	return g.Set
}

// setGate is applyGateToggleResult's own no-optimistic-flip exception: it
// locally overwrites the named gate's Set field, run only once the RPC
// has actually replied. A gate not yet present in m.reply.Gates (the core
// has never reported it) is appended so the modal has something to show
// even before the core's first tick observes it.
func (m *Model) setGate(name string, set bool) {
	for i, g := range m.reply.Gates {
		if g.Name == name {
			m.reply.Gates[i].Set = set
			return
		}
	}
	m.reply.Gates = append(m.reply.Gates, Gate{Name: name, Set: set})
}

// renderGatesModal lists BOTH of INV-LIFE-2's two OR-effective named gates
// (operator-paused, cicd-down -- ADR 0026's hyphenated display form) with
// state/since/owner, regardless of whether the core has ever reported
// either [design: Task 4.8 Files]. R = resume-all is named in the modal's
// own footer.
//
// The Left column's guaranteed gap from the status text in Right is
// render.Modal's own job now [pg2-y6sy5]: it used to pad Left to a fixed
// 12 columns, which happened to equal len("quota-paused") -- this gate's
// old name -- exactly, so that name received ZERO padding and ran straight
// into the status text with no gap at all ("quota-pausedclear since -
// (owner: -)"). render.Modal now sizes that column from the actual Left
// values in play (mirroring
// legendRows' dynamic-width pattern, pg2-58ecs's fix for the same
// fixed-width-column collision shape), so callers here need not pad
// displayName themselves.
func (m *Model) renderGatesModal() string {
	rows := []render.ModalRow{
		m.gateModalRow("operator-paused", core.GateOperatorPaused),
		m.gateModalRow("cicd-down", core.GateCICDDown),
	}
	return render.Modal("Gates", rows, "[R] resume all", m.width, m.height, m.modalScrollOffset)
}

// gateModalRow renders one gate's name/state/since/owner line. displayName
// is the ADR-0026-safe, hyphenated form the operator-facing docs use;
// wireName is the underscored wire name reply.go's Gate.Name actually
// carries (core.GateOperatorPaused / core.GateCICDDown).
//
// A gate that has never been observed by the core, or has been observed
// and is currently clear, is rendered as the unambiguous "not set" --
// never "clear since - (owner: -)": composeStatusReply's statusGates only
// ever populates mtime/owner while a gate is actually SET (an unset gate
// has no mtime, and no writer sets Owner at all today), so the dashes in
// that placeholder text carried no information distinguishing "never
// observed" from "observed and cleared" -- both looked like malformed data
// rather than a plain "not set" fact [pg2-y6sy5].
func (m *Model) gateModalRow(displayName, wireName string) render.ModalRow {
	g, _ := m.gate(wireName)
	right := "not set"
	if g.Set {
		since := "-"
		if !g.Mtime.IsZero() {
			since = g.Mtime.Format(time.RFC3339)
		}
		owner := g.Owner
		if owner == "" {
			owner = "-"
		}
		right = fmt.Sprintf("SET since %s (owner: %s)", since, owner)
	}
	return render.ModalRow{
		Left:  displayName,
		Right: right,
	}
}
