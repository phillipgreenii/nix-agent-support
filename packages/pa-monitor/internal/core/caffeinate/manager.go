// Package caffeinate decides when pa-monitor wants the Mac held awake and asks
// pg-keep-awake to hold it. pa-monitor keeps the mode toggle, the busy
// detection and the grace period; spawning caffeinate and the low-battery rule
// belong to pg-keep-awake, which pa-monitor reaches only through its CLI.
package caffeinate

import (
	"time"
)

type State int

const (
	StateOff State = iota
	StateArmedRunning
	StateArmedCountdown
	// StateError means pa-monitor is armed (toggle on) and wanted the Mac held
	// but could not get it held: an acquire call failed, or `pg-keep-awake
	// status --check` reports the keeper degraded. It is observable as
	// process=error so a broken keep-awake is visible rather than silently
	// presenting as "off".
	StateError
)

// DefaultAcquireEvery is how often acquire is repeated while pa-monitor wants
// the Mac awake, so a wiped state directory heals.
const DefaultAcquireEvery = time.Minute

type Manager struct {
	Grace  time.Duration
	Keeper Keeper
	Now    func() time.Time
	// PID is the process the lease is bound to: the pa-monitor daemon itself,
	// so the lease dies with the daemon.
	PID int
	// AcquireEvery is the acquire repeat period while pa-monitor wants the Mac
	// awake. Zero means DefaultAcquireEvery.
	AcquireEvery time.Duration

	state        State
	toggle       bool
	countdownEnd time.Time
	spawnErr     error

	// lastAcquire is when acquire last succeeded; the repeat is measured from it.
	lastAcquire time.Time
	// obs is the keeper's reading from the most recent Tick.
	obs Observation
}

func (m *Manager) State() State { return m.state }

// SpawnErr returns the error behind StateError: the failed acquire call, or
// ErrKeeperDegraded. Nil unless State() == StateError. (The name predates the
// keeper, when the error came from spawning caffeinate.)
func (m *Manager) SpawnErr() error { return m.spawnErr }

// SetToggle applies the user's mode toggle. Turning it off releases the lease
// at once and drops to StateOff.
func (m *Manager) SetToggle(on bool) {
	m.toggle = on
	if !on {
		if m.state != StateOff {
			m.release()
			m.state = StateOff
		}
		m.spawnErr = nil
	}
}

// Shutdown releases the lease if pa-monitor holds one. It is for daemon exit:
// the lease is also bound to the daemon's pid, so this only makes the release
// immediate instead of waiting for the keeper to notice the exit.
func (m *Manager) Shutdown() {
	if m.state != StateOff {
		m.release()
		m.state = StateOff
		m.spawnErr = nil
	}
}

// GraceRemaining returns the time until pa-monitor would release the lease.
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

// Held reports whether the keeper says the Mac is being held awake right now
// (status --json assertion.held). False when the status could not be read.
func (m *Manager) Held() bool { return m.obs.StatusKnown && m.obs.Held }

// KeeperUp reports whether `pg-keep-awake status --check` found the keeper
// running (exit 0 or 6). False when it is down (exit 5) or the check could not
// run. Meaningful after the first Tick.
func (m *Manager) KeeperUp() bool { return m.obs.Health.Up() }

// ProcessState is the state shown as `process`: State(), except that while the
// keeper withholds the assertion for a low battery (the lease is live but the
// Mac is not held, gate no_spawn or released) it reads StateOff, as it did when
// pa-monitor held the battery rule itself.
func (m *Manager) ProcessState() State {
	if (m.state == StateArmedRunning || m.state == StateArmedCountdown) &&
		m.obs.StatusKnown && !m.obs.Held && m.obs.Gate.Restricts() {
		return StateOff
	}
	return m.state
}

func (m *Manager) acquireEvery() time.Duration {
	if m.AcquireEvery > 0 {
		return m.AcquireEvery
	}
	return DefaultAcquireEvery
}

// Tick advances the state machine. keepAwake is true when the Mac should be
// held awake this tick — any session is Working (D1/D2) OR there is an
// unattempted nudgeable disrupt (D5). The caller computes the disjunction
// inline from the tree + watermark store (NOT the nudger's pending-store,
// which reconciles later in the same tick).
//
// Tick reads the keeper once per call (status --json --check), toggle on or
// off, so the held state, gate and keeper health are always current. The
// keeper, not pa-monitor, decides whether a live lease becomes a held
// assertion: a low battery withholds it whatever keepAwake says.
func (m *Manager) Tick(keepAwake bool) {
	m.obs = m.Keeper.Observe()
	if !m.toggle {
		return
	}
	now := m.Now()
	switch m.state {
	case StateOff:
		// Only acquire when something needs the Mac awake; if toggle was just
		// turned on with nothing active, or grace expired and sessions are
		// still idle, stay off until work resumes — avoids a perpetual
		// acquire/release cycle.
		if !keepAwake {
			return
		}
		m.acquire(now)
	case StateArmedRunning:
		if !keepAwake {
			m.state = StateArmedCountdown
			m.countdownEnd = now.Add(m.Grace)
			break
		}
		m.refresh(now)
	case StateArmedCountdown:
		if keepAwake {
			m.state = StateArmedRunning
			m.refresh(now)
			break
		}
		if !now.Before(m.countdownEnd) {
			m.release()
			m.state = StateOff
			return
		}
		m.refresh(now)
	case StateError:
		// A prior acquire failed, or the keeper is degraded. Retry while the Mac
		// still needs to be awake; otherwise drop the error and fall back to Off
		// (nothing to hold), releasing any lease a degraded keeper was sitting on.
		if !keepAwake {
			m.release()
			m.spawnErr = nil
			m.state = StateOff
			return
		}
		m.acquire(now)
	}
	m.applyDegraded()
}

// applyDegraded maps a degraded keeper (status --check exit 6) to StateError.
func (m *Manager) applyDegraded() {
	if m.obs.Health != HealthDegraded {
		return
	}
	if m.state == StateArmedRunning || m.state == StateArmedCountdown {
		m.spawnErr = ErrKeeperDegraded
		m.state = StateError
	}
}

// acquire asks the keeper to hold the Mac, recording the outcome as either
// StateArmedRunning (success) or StateError (failure). Capturing the error makes
// a broken keep-awake observable as process=error instead of silently reverting
// to "off". The keeper is never waited on for the hold itself: acquire only
// writes a lease file, so it succeeds while the keeper is down.
func (m *Manager) acquire(now time.Time) {
	if err := m.Keeper.Acquire(m.PID); err != nil {
		m.spawnErr = err
		m.state = StateError
		return
	}
	m.lastAcquire = now
	m.spawnErr = nil
	m.state = StateArmedRunning
}

// refresh repeats acquire once per AcquireEvery while the lease is wanted. A
// failure is StateError, as for the first acquire.
func (m *Manager) refresh(now time.Time) {
	if now.Sub(m.lastAcquire) < m.acquireEvery() {
		return
	}
	if err := m.Keeper.Acquire(m.PID); err != nil {
		m.spawnErr = err
		m.state = StateError
		return
	}
	m.lastAcquire = now
}

// release removes the lease. A failure is ignored: the lease is bound to the
// daemon's pid, so it cannot outlive the daemon, and the next tick observes
// whatever the keeper still holds.
func (m *Manager) release() { _ = m.Keeper.Release() }
