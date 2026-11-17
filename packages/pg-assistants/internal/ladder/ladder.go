// Package ladder is the pure ladder engine. Given a session's ladder state,
// this tick's meter readings and the time, Step decides the one action to
// take now (nudge, interrupt, kill, force kill or a graceful stop) and the
// next state. It does no I/O and reads no clock: the caller passes the time
// in, so it needs no context.
//
// The rules:
//
//   - Each session has one ladder state. A rung fires once and the ladder only
//     moves up; a rung counts as fired even when its action fails, so the
//     caller advances the state whether or not the action succeeds.
//   - Priority: an exit request, then a disabled assistant (or an orphan
//     session), then the meters. A change of ladder keeps the rung and the time
//     it fired, so the next time step counts from then. Once an exit request is
//     seen the session stays on the exit ladder.
//   - Exit ladder: below the kill rung a graceful stop; at or past it, a force
//     kill on every call.
//   - Disabled ladder: a nudge on the first call, an interrupt grace.interrupt
//     after the nudge, a kill grace.kill after the interrupt, then a force kill
//     on every call. The meters play no part.
//   - Meter ladder: the target is the highest rung any meter reaches against
//     its own points, and the next rung up fires while the target is above the
//     current rung. Because a meter may stop growing (context only grows during
//     a turn), the disabled ladder's time steps apply as well, with or without
//     meters; after a kill, a force kill on every call. A session that was never
//     nudged and has no meter at its nudge point is left alone.
package ladder

import (
	"fmt"
	"math"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

// Kind is the ladder a session is on.
type Kind int

// The ladders. KindNone is the state before the first Step.
const (
	KindNone Kind = iota
	KindMeter
	KindDisabled
	KindExit
)

func (k Kind) String() string {
	switch k {
	case KindNone:
		return "none"
	case KindMeter:
		return "meter"
	case KindDisabled:
		return "disabled"
	case KindExit:
		return "exit"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Rung is the highest step a session's ladder has fired, in firing order.
type Rung int

// The rungs. RungForceKill is reached after a kill (or a graceful stop) when
// the session is still running.
const (
	RungNone Rung = iota
	RungNudge
	RungInterrupt
	RungKill
	RungForceKill
)

func (r Rung) String() string {
	switch r {
	case RungNone:
		return "none"
	case RungNudge:
		return "nudge"
	case RungInterrupt:
		return "interrupt"
	case RungKill:
		return "kill"
	case RungForceKill:
		return "force-kill"
	}
	return fmt.Sprintf("Rung(%d)", int(r))
}

// Action is what the caller does now.
type Action int

// The actions. Their names are the daemon's event names.
const (
	ActNone Action = iota
	ActNudge
	ActInterrupt
	ActKill
	ActForceKill
	ActGracefulStop
)

func (a Action) String() string {
	switch a {
	case ActNone:
		return "none"
	case ActNudge:
		return "nudge"
	case ActInterrupt:
		return "interrupt"
	case ActKill:
		return "kill"
	case ActForceKill:
		return "force_kill"
	case ActGracefulStop:
		return "graceful_stop"
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

// State is one session's ladder state. The zero value is a session no rung
// has fired for.
type State struct {
	Kind Kind
	Rung Rung
	At   time.Time // when the current rung fired
}

// Meter is one fraction with its own nudge, interrupt and kill points: the
// context fill (tokens over the assistant's limit) or an account block
// (percent used over 100).
type Meter struct {
	Name     string // "context", "five_hour", "seven_day"
	Fraction float64
	Points   config.Points
	Detail   string // e.g. "83000 of 200000 tokens"; empty for none
}

// Input is what one Step sees.
type Input struct {
	Now           time.Time
	Enabled       bool    // false also for an orphan
	ExitRequested bool    // true while the request is on file
	Meters        []Meter // only the meters with a current reading
}

// Grace holds the time steps: Interrupt is the wait from a nudge to the
// interrupt, Kill the wait from the interrupt to the kill.
type Grace struct{ Interrupt, Kill time.Duration }

// Reason prefixes for the ladders whose reasons are not a meter's.
const (
	disabledReason = "disabled"
	exitReason     = "exit requested"
)

// Step returns the next state, the one action to take now (or ActNone), and a
// reason naming the meter that set the target, e.g. "context at 81% (162000
// of 200000 tokens)", or the ladder or time step that fired. The reason is
// empty for ActNone. The caller advances the state whether or not the action
// succeeds.
func Step(s State, in Input, g Grace) (State, Action, string) {
	switch {
	case s.Kind == KindExit || in.ExitRequested:
		s.Kind = KindExit
		if s.Rung < RungKill {
			return fire(s, RungKill, in.Now), ActGracefulStop, exitReason
		}
		return fire(s, RungForceKill, in.Now), ActForceKill, exitReason + ": " + stillRunning
	case !in.Enabled:
		s.Kind = KindDisabled
		// The disabled ladder nudges on the first call, whatever the meters say.
		return climb(s, in.Now, g, RungNudge, disabledReason, disabledReason+": ")
	default:
		s.Kind = KindMeter
		target, why := target(in.Meters)
		return climb(s, in.Now, g, target, why, "")
	}
}

const stillRunning = "still running after the stop"

// climb moves s at most one rung toward target, or along a time step, or to a
// force kill once the kill has fired. why is the reason for a step toward
// target; prefix starts the reason for every other step.
func climb(s State, now time.Time, g Grace, target Rung, why, prefix string) (State, Action, string) {
	switch {
	case s.Rung >= RungKill:
		return fire(s, RungForceKill, now), ActForceKill, prefix + stillRunning
	case target > s.Rung:
		next := s.Rung + 1
		return fire(s, next, now), climbAction(next), why
	case s.Rung == RungNudge && now.Sub(s.At) >= g.Interrupt:
		return fire(s, RungInterrupt, now), ActInterrupt,
			fmt.Sprintf("%sno exit %s after the nudge", prefix, g.Interrupt)
	case s.Rung == RungInterrupt && now.Sub(s.At) >= g.Kill:
		return fire(s, RungKill, now), ActKill,
			fmt.Sprintf("%sno exit %s after the interrupt", prefix, g.Kill)
	}
	return s, ActNone, ""
}

// climbAction is the action that fires rung r on the way up.
func climbAction(r Rung) Action {
	switch r {
	case RungNudge:
		return ActNudge
	case RungInterrupt:
		return ActInterrupt
	default:
		return ActKill
	}
}

// fire moves s to rung r. At changes only when the rung does, so a repeated
// force kill keeps the time the first one fired.
func fire(s State, r Rung, now time.Time) State {
	if r != s.Rung {
		s.Rung, s.At = r, now
	}
	return s
}

// target is the highest rung any meter reaches, with the reason naming the
// first meter that reaches it.
func target(meters []Meter) (Rung, string) {
	best, why := RungNone, ""
	for _, m := range meters {
		if r := m.rung(); r > best {
			best, why = r, m.reason()
		}
	}
	return best, why
}

// rung is the highest rung m reaches against its own points. A meter without
// points (Nudge not above 0, which config validation never lets through) sets
// no target, so a zero Points cannot kill a session.
func (m Meter) rung() Rung {
	p := m.Points
	switch {
	case !(p.Nudge > 0):
		return RungNone
	case m.Fraction >= p.Kill:
		return RungKill
	case m.Fraction >= p.Interrupt:
		return RungInterrupt
	case m.Fraction >= p.Nudge:
		return RungNudge
	}
	return RungNone
}

// reason names m and its whole percent, rounded down so a fraction just below
// a point never reads as that point (the small epsilon absorbs float error,
// so 0.29 reads 29%, not 28%).
func (m Meter) reason() string {
	pct := int(math.Floor(m.Fraction*100 + 1e-9))
	if m.Detail == "" {
		return fmt.Sprintf("%s at %d%%", m.Name, pct)
	}
	return fmt.Sprintf("%s at %d%% (%s)", m.Name, pct, m.Detail)
}
