// Package tui implements pg-router's operator-facing terminal UI. This file
// (Task 4.8, reworked by bead pg2-h63eu for the Gate Registry) carries the
// command-pattern P gate toggle, its R = resume-all sub-binding, and the gates
// modal (g) that lists every gate currently in force.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// gateToggleTimeout bounds one ToggleGate round trip from the Model's
// side -- the same safety-net role pollTimeout plays above the Poller's
// own internal deadline (poll.go's pollTimeout above poller.go's
// pollerRPCDeadline). A core that never replies cannot pin the pending
// indicator forever: the RPC deadline is what clears it with a warn flash
// [design: Task 4.8 Step 3].
const gateToggleTimeout = 10 * time.Second

// ToggleVerbResumeAll is the Poller.ToggleGate verb behind the Gates modal's
// R binding: the socket `resume` verb with `all` set, clearing EVERY active
// gate, not just SYSTEM_PAUSE.
const ToggleVerbResumeAll = "resume-all"

// gateToggleResultMsg carries ToggleGate's outcome back to Update
// [design: Task 4.8 Interfaces].
type gateToggleResultMsg struct {
	verb      string
	effective string
	err       error
}

// handleToggleOperatorGate implements the P key [design: Task 4.8 Files]: a
// command-pattern toggle of the SYSTEM_PAUSE gate ONLY -- never another
// system's gate, which a bare pause/resume deliberately cannot reach (resume
// clears only SYSTEM_PAUSE unless `all` is given, so a gate another system
// owns is never cleared by accident). It calls m.poller.ToggleGate(ctx,
// verb), never a raw *core.Client, via startGateToggle.
//
// No optimistic flip, ever [design: Task 4.8 Step 1]: this method only
// resolves which verb to send and stamps the pending indicator: it never
// itself changes m.reply's gate state. That happens in exactly one place,
// applyGateToggleResult, and only once the RPC has actually replied.
func (m *Model) handleToggleOperatorGate() tea.Cmd {
	verb := core.SubcommandPause
	if m.gateSet(core.GateSystemPause) {
		verb = core.SubcommandResume
	}
	return m.startGateToggle(verb)
}

// handleResumeAllGates implements the "R = resume-all inside the Gates
// modal" sub-binding [design: Task 4.8 Files]: a no-op everywhere except
// while the Gates modal is open. It clears EVERY active gate (any caller may
// clear any gate), via the socket `resume` verb's `all` flag.
func handleResumeAllGates(m *Model) tea.Cmd {
	if m.activeModal != ModalGates {
		return nil
	}
	return m.startGateToggle(ToggleVerbResumeAll)
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
		return gateToggleResultMsg{verb: verb, effective: effective, err: err}
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
// state (the no-optimistic-flip contract's other half) -- it applies the
// toggle locally so the operator sees the new state immediately rather than
// waiting for the next poll tick, and flashes the resulting EFFECTIVE state,
// not just the toggled gate: clearing SYSTEM_PAUSE while another gate remains
// active must not imply the pool resumed [design: Task 4.8 (worked flash
// example)].
func (m *Model) applyGateToggleResult(msg gateToggleResultMsg) tea.Cmd {
	m.gateTogglePending = false
	if msg.err != nil {
		m.errorLogger.LogString("gate toggle failed: " + msg.err.Error())
		m.setFlash("operator gate toggle failed: "+msg.err.Error(), FlashWarn)
		return m.flashClearCmd()
	}
	switch {
	case msg.effective == "paused":
		m.setGate(core.GateSystemPause, true)
	case msg.verb == ToggleVerbResumeAll:
		m.reply.Gates = nil
	default:
		m.setGate(core.GateSystemPause, false)
	}
	m.setFlash(m.operatorGateFlashText(msg.effective), FlashInfo)
	return m.flashClearCmd()
}

// operatorGateFlashText names the resulting EFFECTIVE state, not just the
// toggled gate [design: Task 4.8 (worked flash example)]: clearing SYSTEM_PAUSE
// while another gate remains active still leaves routing blocked for the
// participants that block on it, and the flash says so rather than implying
// the pool resumed.
func (m *Model) operatorGateFlashText(effective string) string {
	if effective == "paused" {
		return core.GateSystemPause + " set — pool now PAUSED"
	}
	if others := m.otherGateTypes(core.GateSystemPause); len(others) > 0 {
		return core.GateSystemPause + " cleared — still gated by " + strings.Join(others, ", ")
	}
	return core.GateSystemPause + " cleared — pool now RESUMED"
}

// otherGateTypes lists the TYPEs of every active gate except except, sorted.
func (m *Model) otherGateTypes(except string) []string {
	var out []string
	for _, g := range m.reply.Gates {
		if g.Type != except {
			out = append(out, textsafe.Sanitize(g.Type))
		}
	}
	sort.Strings(out)
	return out
}

// gate looks up the named gate TYPE in the last-polled reply, reporting
// whether it is active. The wire lists ACTIVE gates only, so ok means "in
// force".
func (m *Model) gate(gateType string) (Gate, bool) {
	for _, g := range m.reply.Gates {
		if g.Type == gateType {
			return g, true
		}
	}
	return Gate{}, false
}

// gateSet reports whether the named gate TYPE is currently active in the
// last-polled reply.
func (m *Model) gateSet(gateType string) bool {
	_, ok := m.gate(gateType)
	return ok
}

// setGate is applyGateToggleResult's own no-optimistic-flip exception: it
// locally adds or removes the named gate in the rendered list, run only once
// the RPC has actually replied.
func (m *Model) setGate(gateType string, set bool) {
	for i, g := range m.reply.Gates {
		if g.Type != gateType {
			continue
		}
		if !set {
			m.reply.Gates = append(m.reply.Gates[:i:i], m.reply.Gates[i+1:]...)
		}
		return
	}
	if set {
		m.reply.Gates = append(m.reply.Gates, Gate{Type: gateType, SetAt: time.Now()})
	}
}

// renderGatesModal lists every gate currently in force -- TYPE, description,
// owner, set-at and TTL remaining -- or an unambiguous "none active" row.
// R = resume-all is named in the modal's own footer.
//
// The Left column's guaranteed gap from the status text in Right is
// render.Modal's own job [pg2-y6sy5]: it sizes that column from the actual
// Left values in play, so gate TYPEs of any length need no padding here.
func (m *Model) renderGatesModal() string {
	return render.Modal("Gates", m.gateModalRows(), "[R] resume all", m.width, m.height, m.modalScrollOffset)
}

// gateModalRows renders one row per active gate (sorted by TYPE), shared by the
// Gates modal and the Problems modal so the two never drift into different
// renderings of the same state. A clear registry renders the explicit "none
// active" -- never a blank section -- mirroring unmatchedBindingRows' "(none)"
// precedent (pg2-y6sy5): an empty section must read as a confirmed fact.
func (m *Model) gateModalRows() []render.ModalRow {
	if len(m.reply.Gates) == 0 {
		return []render.ModalRow{{Left: "gates", Right: "none active"}}
	}
	gates := append([]Gate(nil), m.reply.Gates...)
	sort.Slice(gates, func(i, j int) bool { return gates[i].Type < gates[j].Type })
	rows := make([]render.ModalRow, 0, len(gates))
	for _, g := range gates {
		rows = append(rows, render.ModalRow{Left: textsafe.Sanitize(g.Type), Right: gateDetail(g)})
	}
	return rows
}

// gateDetail renders one gate's TTL / set-at / owner / description line. The
// fixed-width facts (TTL left, set-at, owner) lead and the free-text
// description trails, because render.Modal hard-clips a long Right value with
// no ellipsis: a narrow terminal then loses the tail of the description, never
// the TTL an operator needs to see.
func gateDetail(g Gate) string {
	ttl := "no TTL"
	if !g.ExpiresAt.IsZero() {
		ttl = "TTL " + formatCoarse(time.Duration(g.TTLRemainingMs)*time.Millisecond) + " left"
	}
	since := "-"
	if !g.SetAt.IsZero() {
		since = g.SetAt.Format("2006-01-02 15:04")
	}
	owner := textsafe.Sanitize(g.Owner)
	if owner == "" {
		owner = "-"
	}
	desc := textsafe.Sanitize(g.Description)
	if desc == "" {
		desc = "(no description)"
	}
	return fmt.Sprintf("%s · set %s · owner: %s · %s", ttl, since, owner, desc)
}
