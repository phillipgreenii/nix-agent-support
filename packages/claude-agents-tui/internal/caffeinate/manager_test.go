package caffeinate

import (
	"testing"
	"time"
)

func TestTogglesOnWithWorkingSpawnsCaffeinate(t *testing.T) {
	spawned := 0
	m := &Manager{
		Grace: 5 * time.Second,
		Spawn: func(pid int) error { spawned++; return nil },
		Kill:  func() error { return nil },
		Now:   func() time.Time { return time.Unix(0, 0) },
	}
	m.SetToggle(true)
	m.Tick(true)
	if spawned != 1 {
		t.Errorf("Spawn called %d times, want 1", spawned)
	}
	if m.State() != StateArmedRunning {
		t.Errorf("State = %v, want ArmedRunning", m.State())
	}
}

func TestGraceCountdownAndExpiry(t *testing.T) {
	var now time.Time
	spawned := 0
	killed := 0
	m := &Manager{
		Grace: 5 * time.Second,
		Spawn: func(pid int) error { spawned++; return nil },
		Kill:  func() error { killed++; return nil },
		Now:   func() time.Time { return now },
	}
	now = time.Unix(0, 0)
	m.SetToggle(true)
	m.Tick(true)
	now = time.Unix(1, 0)
	m.Tick(false)
	if m.State() != StateArmedCountdown {
		t.Errorf("State = %v, want ArmedCountdown", m.State())
	}
	now = time.Unix(10, 0)
	m.Tick(false)
	if m.State() != StateOff {
		t.Errorf("State = %v, want Off after grace expired", m.State())
	}
	if killed != 1 {
		t.Errorf("Kill called %d times, want 1", killed)
	}
}

func TestRespawnOnReactivate(t *testing.T) {
	var now time.Time
	spawned := 0
	m := &Manager{
		Grace: 5 * time.Second,
		Spawn: func(pid int) error { spawned++; return nil },
		Kill:  func() error { return nil },
		Now:   func() time.Time { return now },
	}
	now = time.Unix(0, 0)
	m.SetToggle(true)
	m.Tick(true)
	now = time.Unix(1, 0)
	m.Tick(false)
	now = time.Unix(2, 0)
	m.Tick(true)
	if spawned != 1 {
		t.Errorf("spawned %d times, want 1 (still alive)", spawned)
	}
	if m.State() != StateArmedRunning {
		t.Errorf("State = %v, want ArmedRunning", m.State())
	}
}

func TestToggleOffKillsImmediately(t *testing.T) {
	killed := 0
	m := &Manager{
		Grace: 60 * time.Second,
		Spawn: func(int) error { return nil },
		Kill:  func() error { killed++; return nil },
		Now:   func() time.Time { return time.Unix(0, 0) },
	}
	m.SetToggle(true)
	m.Tick(true)
	m.SetToggle(false)
	if killed != 1 {
		t.Errorf("kill count = %d, want 1", killed)
	}
	if m.State() != StateOff {
		t.Errorf("State = %v, want Off", m.State())
	}
}
