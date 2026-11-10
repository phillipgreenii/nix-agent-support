package serve

import (
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
)

// Daemon.Close closes the process table when it can be closed, so the kqueue
// loop (started by the first watch) is gone afterwards.
//
// The real table is the thing under test, so the fake proc.System of the other
// tests cannot stand in: only the real one has a kqueue loop whose goroutine
// can leak. The 1 s (measured 1.02 s plain, 1.11 s -race) is Real.Close waiting
// for that loop's own one-second wake, a production interval; it is not a test
// number and stays.
func TestDaemonCloseStopsTheProcessTableLoop(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	e := newEnv(t, false)
	real := proc.NewReal()
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: real})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	stop, err := real.Watch(os.Getpid(), func(time.Time) {}) // starts the loop where there is one
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := goleak.Find(opt); err != nil {
		t.Fatalf("a process-table goroutine outlived Daemon.Close: %v", err)
	}
}
