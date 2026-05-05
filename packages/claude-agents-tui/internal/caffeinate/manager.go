package caffeinate

import "time"

type State int

const (
	StateOff State = iota
	StateArmedRunning
	StateArmedCountdown
)

type Manager struct {
	Grace time.Duration
	Spawn func(tuiPID int) error
	Kill  func() error
	Now   func() time.Time
	PID   int

	state        State
	toggle       bool
	countdownEnd time.Time
}

func (m *Manager) State() State { return m.state }

func (m *Manager) SetToggle(on bool) {
	m.toggle = on
	if !on {
		if m.state != StateOff {
			_ = m.Kill()
			m.state = StateOff
		}
	}
}

// GraceRemaining returns the time until caffeinate would auto-expire.
// Zero when not in countdown.
func (m *Manager) GraceRemaining() time.Duration {
	if m.state != StateArmedCountdown {
		return 0
	}
	rem := m.countdownEnd.Sub(m.Now())
	if rem < 0 {
		return 0
	}
	return rem
}

// Tick advances the state machine.
func (m *Manager) Tick(anyWorking bool) {
	if !m.toggle {
		return
	}
	now := m.Now()
	switch m.state {
	case StateOff:
		_ = m.Spawn(m.PID)
		if anyWorking {
			m.state = StateArmedRunning
		} else {
			m.state = StateArmedCountdown
			m.countdownEnd = now.Add(m.Grace)
		}
	case StateArmedRunning:
		if !anyWorking {
			m.state = StateArmedCountdown
			m.countdownEnd = now.Add(m.Grace)
		}
	case StateArmedCountdown:
		if anyWorking {
			m.state = StateArmedRunning
			return
		}
		if !now.Before(m.countdownEnd) {
			_ = m.Kill()
			m.state = StateOff
		}
	}
}
