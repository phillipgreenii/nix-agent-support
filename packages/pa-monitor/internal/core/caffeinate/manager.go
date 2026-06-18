package caffeinate

import "time"

type State int

const (
	StateOff State = iota
	StateArmedRunning
	StateArmedCountdown
	// StateError means the most recent Spawn attempt failed; the manager is
	// armed (toggle on) and wanted to hold the assertion but could not. It is
	// observable as process=error so a broken caffeinate is visible rather
	// than silently presenting as "off".
	StateError
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
	spawnErr     error
}

func (m *Manager) State() State { return m.state }

// SpawnErr returns the error from the most recent failed Spawn attempt, or
// nil when the last spawn succeeded / none has been attempted. Non-nil iff
// State() == StateError.
func (m *Manager) SpawnErr() error { return m.spawnErr }

func (m *Manager) SetToggle(on bool) {
	m.toggle = on
	if !on {
		if m.state != StateOff {
			_ = m.Kill()
			m.state = StateOff
		}
		m.spawnErr = nil
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
	// If caffeinate died unexpectedly, reset so it can be re-spawned. Skip
	// StateError: no process was ever started there, so an IsAlive==false is
	// expected and must not mask the error by collapsing it to Off.
	if m.state != StateOff && m.state != StateError && m.IsAlive != nil && !m.IsAlive() {
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
		m.trySpawn()
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
	case StateError:
		// A prior Spawn failed. Retry while the Mac still needs to be awake;
		// otherwise release the error and fall back to Off (nothing to hold).
		if !keepAwake {
			m.spawnErr = nil
			m.state = StateOff
			return
		}
		m.trySpawn()
	}
}

// trySpawn attempts to start the caffeinate subprocess, recording the outcome
// as either StateArmedRunning (success) or StateError (failure). Capturing the
// spawn error makes a broken caffeinate observable as process=error instead of
// silently reverting to "off".
func (m *Manager) trySpawn() {
	if err := m.Spawn(m.PID); err != nil {
		m.spawnErr = err
		m.state = StateError
		return
	}
	m.spawnErr = nil
	m.state = StateArmedRunning
}
