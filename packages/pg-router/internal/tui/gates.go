// Package tui implements pg-router's operator-facing terminal UI. This file
// carries the command-pattern P toggle of the router PAUSED level, its
// R = resume-all sub-binding (clear every operator override), and the levels
// modal (g) that lists every resource's level in force. The toggle is the socket
// verbs level-set / level-clear on the router resource.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// gateToggleTimeout bounds one ToggleGate round trip from the Model's
// side -- the same safety-net role pollTimeout plays above the Poller's
// own internal deadline (poll.go's pollTimeout above poller.go's
// pollerRPCDeadline). A core that never replies cannot pin the pending
// indicator forever: the RPC deadline is what clears it with a warn flash.
const gateToggleTimeout = 10 * time.Second

// ToggleVerbResumeAll is the Poller.ToggleGate verb behind the levels modal's
// R binding: level-clear with `all`, removing EVERY operator override.
const ToggleVerbResumeAll = "resume-all"

// The other two Poller.ToggleGate verbs. The wire verbs behind them are the
// level verbs level-set / level-clear on the router resource (see
// SocketPoller.ToggleGate).
const (
	ToggleVerbPause  = "pause"
	ToggleVerbResume = "resume"
)

// routerResource and levelPaused name the router pause: the router resource at
// its PAUSED level.
const (
	routerResource = "router"
	levelPaused    = "PAUSED"
	levelRunning   = "RUNNING"
)

// gateToggleResultMsg carries ToggleGate's outcome back to Update.
type gateToggleResultMsg struct {
	verb      string
	effective string
	err       error
}

// handleToggleOperatorGate implements the P key: a command-pattern toggle of
// the router PAUSED level ONLY -- a bare pause / resume deliberately cannot touch
// a requirement of a registration, which only the resource's own level decides.
// It calls m.poller.ToggleGate(ctx, verb), never a raw *core.Client, via
// startGateToggle.
//
// No optimistic flip, ever: this method only resolves which verb to send and
// stamps the pending indicator: it never itself changes m.reply's levels. That
// happens in exactly one place, applyGateToggleResult, and only once the RPC has
// actually replied.
func (m *Model) handleToggleOperatorGate() tea.Cmd {
	verb := ToggleVerbPause
	if m.routerPaused() {
		verb = ToggleVerbResume
	}
	return m.startGateToggle(verb)
}

// handleResumeAllGates implements the "R = resume all inside the levels modal"
// sub-binding: a no-op everywhere except while the levels modal is open. It
// clears EVERY operator override (any caller may clear any), via level-clear
// with `all`.
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

// applyGateToggleResult is Update's gateToggleResultMsg handler. It always
// clears the pending indicator.
//
// On failure (an RPC error, including the gateToggleTimeout deadline firing
// with no reply): the rendered level state is left exactly where it was, a warn
// flash names the failure, and the failure is logged.
//
// On success: this is the ONE place allowed to change the rendered level state
// (the no-optimistic-flip contract's other half) -- it applies the toggle locally
// so the operator sees the new state immediately rather than waiting for the next
// poll tick, and flashes the resulting EFFECTIVE state, not just the toggle:
// resuming the router while another resource still blocks a role must not imply
// the pool resumed.
func (m *Model) applyGateToggleResult(msg gateToggleResultMsg) tea.Cmd {
	m.gateTogglePending = false
	if msg.err != nil {
		m.errorLogger.LogString("level toggle failed: " + msg.err.Error())
		m.setFlash("operator level toggle failed: "+msg.err.Error(), FlashWarn)
		return m.flashClearCmd()
	}
	switch {
	case msg.effective == "paused":
		m.setRouterPaused(true)
	case msg.verb == ToggleVerbResumeAll:
		for i := range m.reply.Levels {
			m.reply.Levels[i].Override = nil
		}
		m.setRouterPaused(false)
	default:
		m.setRouterPaused(false)
	}
	m.setFlash(m.operatorGateFlashText(msg.effective), FlashInfo)
	return m.flashClearCmd()
}

// operatorGateFlashText names the resulting EFFECTIVE state, not just the
// toggle: resuming the router while another resource still blocks a role leaves
// that role held, and the flash says so rather than implying the pool resumed.
func (m *Model) operatorGateFlashText(effective string) string {
	if effective == "paused" {
		return "router PAUSED — admission stopped for every role"
	}
	if others := m.otherBlockers(); len(others) > 0 {
		return "router RUNNING — roles still held by " + strings.Join(others, ", ")
	}
	return "router RUNNING — pool now RESUMED"
}

// otherBlockers lists the resources, other than the router, that block a role
// right now (a failing clause that is a reading, not "no reading"), sorted.
func (m *Model) otherBlockers() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range m.reply.PausedRoles {
		for _, f := range p.Failing {
			if f.Resource != routerResource && f.Kind != "unknown" && !seen[f.Resource] {
				seen[f.Resource] = true
				out = append(out, textsafe.Sanitize(f.Resource))
			}
		}
	}
	sort.Strings(out)
	return out
}

// routerPaused reports whether the router resource is at PAUSED in the
// last-polled reply.
func (m *Model) routerPaused() bool {
	for _, l := range m.reply.Levels {
		if l.Resource == routerResource {
			return l.Level == levelPaused
		}
	}
	return false
}

// setRouterPaused is applyGateToggleResult's own no-optimistic-flip exception: it
// locally sets the router row to PAUSED or RUNNING (adding the row when the
// reply had none), run only once the RPC has actually replied.
func (m *Model) setRouterPaused(paused bool) {
	level := levelRunning
	if paused {
		level = levelPaused
	}
	for i, l := range m.reply.Levels {
		if l.Resource == routerResource {
			m.reply.Levels[i].Level = level
			m.reply.Levels[i].Since = time.Now()
			return
		}
	}
	m.reply.Levels = append(m.reply.Levels, LevelRow{Resource: routerResource, Level: level, Source: "operator", Since: time.Now()})
}

// renderGatesModal lists every resource's level in force -- the resource, its
// level, the reading, the source and since when -- or "no reading" for a silent
// one. R = resume all is named in the modal's own footer.
//
// The Left column's guaranteed gap from the status text in Right is
// render.Modal's own job: it sizes that column from the actual Left values in
// play, so resource names of any length need no padding here.
func (m *Model) renderGatesModal() string {
	return render.Modal("Levels", m.levelModalRows(), "[R] resume all", m.width, m.height, m.modalScrollOffset)
}

// levelModalRows renders one row per resource (sorted by name). An empty reply
// renders the explicit "none" -- never a blank section: an empty section must
// read as a confirmed fact.
func (m *Model) levelModalRows() []render.ModalRow {
	if len(m.reply.Levels) == 0 {
		return []render.ModalRow{{Left: "levels", Right: "none configured"}}
	}
	rows := append([]LevelRow(nil), m.reply.Levels...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Resource < rows[j].Resource })
	out := make([]render.ModalRow, 0, len(rows))
	for _, l := range rows {
		out = append(out, render.ModalRow{Left: textsafe.Sanitize(l.Resource), Right: levelDetail(l)})
	}
	return out
}

// levelDetail renders one resource's line. The level, its numbers and any
// override lead and the since time trails, because render.Modal hard-clips a long Right value with
// no ellipsis: a narrow terminal then loses the tail, never the level.
func levelDetail(l LevelRow) string {
	if l.Level == "" {
		if l.NoSensor {
			return "no sensor configured"
		}
		return "no reading (sensor has no answer)"
	}
	s := textsafe.Sanitize(l.Level)
	if l.Reading != nil {
		s += fmt.Sprintf(" · %g %s", l.Reading.Value, textsafe.Sanitize(l.Reading.Unit))
	}
	if l.Source == "operator" && l.Override != nil {
		if l.Override.UntilCleared {
			s += " · OVERRIDE by " + textsafe.Sanitize(l.Owner) + ", until cleared"
		} else {
			s += " · OVERRIDE by " + textsafe.Sanitize(l.Owner) + ", expires " + l.Override.ExpiresAt.UTC().Format("2006-01-02 15:04") + "Z"
		}
	} else if l.Owner != "" {
		s += " · " + textsafe.Sanitize(l.Owner)
	}
	if !l.Since.IsZero() {
		s += " · since " + l.Since.UTC().Format("2006-01-02 15:04") + "Z"
	}
	if l.Stale {
		s += " · stale"
	}
	return s
}
