// Package connector is the one process-spawn client every pg-desk call of
// pg-connector (and of the router binary the focus announcement hands an
// event to) goes through. It sits over cmdrun.Runner: production runs
// cmdrun.Exec, and a test swaps in an in-process cmdruntest.Fake through Use
// instead of writing a shell script onto PATH.
//
// Run is the single place a call is bounded: it gives every call its own
// process group (a context end kills the whole group), a graceful SIGTERM
// before SIGKILL, and a Timeout when the caller set none.
package connector

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

const (
	// DefaultTimeout bounds a call whose caller set no Timeout.
	DefaultTimeout = 60 * time.Second
	// TermGrace is how long a cancelled or timed-out child gets to exit on
	// SIGTERM before SIGKILL.
	TermGrace = 2 * time.Second
)

var (
	mu     sync.RWMutex
	runner cmdrun.Runner = cmdrun.Exec
)

// Run runs c through the installed runner after bounding it (see the package
// comment). It is itself a cmdrun.Runner.
func Run(ctx context.Context, c cmdrun.Call) cmdrun.Result {
	c.OwnProcGroup = true
	if c.TermGrace == 0 {
		c.TermGrace = TermGrace
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	mu.RLock()
	r := runner
	mu.RUnlock()
	return r(ctx, c)
}

// Use installs r as the runner behind Run and returns a function that
// restores the previous one. Tests call it with a cmdruntest.Fake runner.
func Use(r cmdrun.Runner) (restore func()) {
	mu.Lock()
	prev := runner
	runner = r
	mu.Unlock()
	return func() {
		mu.Lock()
		runner = prev
		mu.Unlock()
	}
}

// ResultError reports why res is not a clean exit 0: a spawn failure, a
// timeout, a context end, a signal, or a non-zero exit (worded like os/exec's
// "exit status N"). Nil for a clean run.
func ResultError(res cmdrun.Result) error {
	switch {
	case res.StartErr != nil:
		return res.StartErr
	case res.TimedOut:
		return fmt.Errorf("timed out: %w", res.CtxErr)
	case res.CtxErr != nil:
		return res.CtxErr
	case res.Signal != "":
		return fmt.Errorf("signal: %s", res.Signal)
	case res.Code != 0:
		return fmt.Errorf("exit status %d", res.Code)
	}
	return nil
}

// Output runs c and returns stdout with ResultError's error
// (exec.Cmd.Output's contract).
func Output(ctx context.Context, c cmdrun.Call) ([]byte, error) {
	res := Run(ctx, c)
	return res.Stdout, ResultError(res)
}

// Combined runs c and returns stdout then stderr with ResultError's error
// (exec.Cmd.CombinedOutput's contract, without interleaving).
func Combined(ctx context.Context, c cmdrun.Call) ([]byte, error) {
	res := Run(ctx, c)
	return bytes.Join([][]byte{res.Stdout, res.Stderr}, nil), ResultError(res)
}
