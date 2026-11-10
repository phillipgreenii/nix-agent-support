package settle

import (
	"testing"
	"time"
)

// A wake that queues for the run lock reports the wait in its phases, apart
// from the work phases, so a slow wake shows whether it waited or worked.
func TestWakeReportsRunLockWaitInPhases(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	e.seed(3, time.Hour)
	e.tick() // the reconciliation
	e.sett.run.Lock()
	done := make(chan error)
	go func() {
		_, err := e.sett.Wake(e.clk.Now())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	e.sett.run.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ph := e.sett.LastPhases(); ph.LockWait < 10*time.Millisecond {
		t.Fatalf("wake lock wait %v, want at least the time the lock was held", ph.LockWait)
	}
	// A wake that finds the lock free reports no wait.
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if ph := e.sett.LastPhases(); ph.LockWait > 5*time.Millisecond {
		t.Fatalf("uncontended wake lock wait %v", ph.LockWait)
	}
}
