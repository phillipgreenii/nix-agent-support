package settle

import "testing"

// The removal pass of a Tick runs through Config.Timer, once per tick; the
// operator remove operation does not.
func TestTickTimesTheRemovalPassAndRemoveDoesNot(t *testing.T) {
	calls := 0
	e := newEnvWith(t, false, func(e *env) {
		e.removeOn = true
		e.extra = func(c *Config) {
			c.Timer = func(step func() error) error { calls++; return step() }
		}
	})
	e.oldSettled(5, removeAge+1)
	e.tick()
	if calls != 1 {
		t.Fatalf("Timer calls after one tick = %d, want 1", calls)
	}
	e.tick()
	if calls != 2 {
		t.Fatalf("Timer calls after two ticks = %d, want 2", calls)
	}
	e.remove(sess)
	if calls != 2 {
		t.Fatalf("the operator remove used the Timer: %d calls", calls)
	}
}

func TestTickWithRemovalOffNeverCallsTheTimer(t *testing.T) {
	calls := 0
	e := newEnvWith(t, false, func(e *env) {
		e.extra = func(c *Config) { c.Timer = func(step func() error) error { calls++; return step() } }
	})
	e.tick()
	if calls != 0 {
		t.Fatalf("Timer calls = %d, want 0", calls)
	}
}
