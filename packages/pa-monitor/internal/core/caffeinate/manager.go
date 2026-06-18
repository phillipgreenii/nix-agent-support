package caffeinate

import "time"

type State int

const (
	StateOff State = iota
	StateArmedRunning
	StateArmedCountdown
)

type Manager struct {
	Grace   time.Duration
	Spawn   func(tuiPID int) error
	Kill    func() error
	IsAlive func() bool // optional; if set, checked each Tick to detect unexpected exits
	Now     func() time.Time
	PID     int

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

// Tick advances the state machine. keepAwake is true when the Mac should be
// held awake this tick — any session is Working (D1/D2) OR there is an
// unattempted nudgeable disrupt (D5). The caller computes the disjunction
// inline from the tree + watermark store (NOT the nudger's pending-store,
// which reconciles later in the same tick).
func (m *Manager) Tick(keepAwake bool) {
	if !m.toggle {
		return
	}
	// If caffeinate died unexpectedly, reset so it can be re-spawned.
	if m.state != StateOff && m.IsAlive != nil && !m.IsAlive() {
		m.state = StateOff
	}
	now := m.Now()
	switch m.state {
	case StateOff:
		// Only spawn when something needs the Mac awake; if toggle was just
		// turned on with nothing active, or grace expired and sessions are
		// still idle, stay off until work resumes — avoids perpetual
		// kill/respawn cycle.
		if !keepAwake {
			return
		}
		_ = m.Spawn(m.PID)
		m.state = StateArmedRunning
	case StateArmedRunning:
		if !keepAwake {
			m.state = StateArmedCountdown
			m.countdownEnd = now.Add(m.Grace)
		}
	case StateArmedCountdown:
		if keepAwake {
			m.state = StateArmedRunning
			return
		}
		if !now.Before(m.countdownEnd) {
			_ = m.Kill()
			m.state = StateOff
		}
	}
}
