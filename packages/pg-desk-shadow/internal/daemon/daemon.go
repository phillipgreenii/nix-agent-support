// Package daemon supervises the shadow run's own pg-connector-github child:
// it starts the daemon through the sandboxed runner, waits until
// `status --json` reports it serving, restarts it when its process dies, takes
// the per-slot health sample the tick rows carry, and stops it at the end.
//
// The package has no compile-time dependency on the daemon module: the binary
// is an argv word and `status --json` is read through its exit code only (0
// healthy, 2 degraded, 3 down).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// Defaults of the behaviours the design left to the implementer.
const (
	// DefaultReadyTimeout bounds the wait for the first healthy or degraded
	// status after a start.
	DefaultReadyTimeout = 60 * time.Second
	// DefaultReadyInterval separates the readiness probes.
	DefaultReadyInterval = 500 * time.Millisecond
	// DefaultStatusTimeout bounds one `status --json` call.
	DefaultStatusTimeout = 30 * time.Second
	// DefaultStopGrace is how long the child gets to drain after SIGTERM.
	DefaultStopGrace = 20 * time.Second
	// DefaultRestartBackoff is the least time between two launches of the
	// child, so a daemon that dies at once is not respawned in a tight loop.
	DefaultRestartBackoff = 10 * time.Second
)

// Process is the sandboxed runner the supervisor drives (*runner.Runner).
type Process interface {
	Start(ctx context.Context, argv ...string) (*runner.Child, error)
	Run(ctx context.Context, timeout time.Duration, argv ...string) (runner.Result, error)
}

// Supervisor owns the daemon child. The zero value of every duration takes its
// default; Runner, Binary and Config are required.
type Supervisor struct {
	Runner Process
	// Binary is the absolute path of the pinned pg-connector-github.
	Binary string
	// Config is the rendered daemon configuration file.
	Config string

	ReadyTimeout   time.Duration
	ReadyInterval  time.Duration
	StatusTimeout  time.Duration
	StopGrace      time.Duration
	RestartBackoff time.Duration

	// Now and Sleep are test seams (defaults: time.Now and a timer wait).
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// Logf receives lifecycle events (nil discards them).
	Logf func(format string, a ...any)

	mu        sync.Mutex
	child     *runner.Child
	restarts  int
	lastStart time.Time
	stopped   bool
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Supervisor) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Supervisor) logf(format string, a ...any) {
	if s.Logf != nil {
		s.Logf(format, a...)
	}
}

func dflt(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (s *Supervisor) serveArgv() []string {
	return []string{s.Binary, "--config", s.Config, "serve"}
}

func (s *Supervisor) statusArgv() []string {
	return []string{s.Binary, "--config", s.Config, "status", "--json"}
}

// Restarts is the number of times the child was relaunched after it died.
func (s *Supervisor) Restarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

// Start launches the child and waits until it is serving (status exit 0 or 2).
// It returns an error, with the child stopped, when the child exits first, the
// wait runs past ReadyTimeout, or ctx ends: the run MUST then refuse to start
// rather than run with no daemon.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.child != nil {
		s.mu.Unlock()
		return errors.New("the daemon is already started")
	}
	s.stopped = false
	if err := s.launchLocked(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	child := s.child
	s.mu.Unlock()

	timeout := dflt(s.ReadyTimeout, DefaultReadyTimeout)
	deadline := s.now().Add(timeout)
	last := "no status yet"
	for {
		select {
		case <-child.Done():
			_ = s.Stop()
			return fmt.Errorf("the daemon exited (code %d) before it was serving; last status: %s (see the daemon log)", child.ExitCode(), last)
		default:
		}
		code, err := s.probe(ctx)
		switch {
		case err != nil:
			last = err.Error()
		case code == 0 || code == 2:
			s.logf("daemon serving (status exit %d)", code)
			return nil
		default:
			last = fmt.Sprintf("status exit %d", code)
		}
		if !s.now().Before(deadline) {
			_ = s.Stop()
			return fmt.Errorf("the daemon was not serving within %s; last status: %s (see the daemon log)", timeout, last)
		}
		if err := s.sleep(ctx, dflt(s.ReadyInterval, DefaultReadyInterval)); err != nil {
			_ = s.Stop()
			return fmt.Errorf("waiting for the daemon: %w", err)
		}
	}
}

// launchLocked starts a child process; s.mu MUST be held.
func (s *Supervisor) launchLocked(ctx context.Context) error {
	c, err := s.Runner.Start(ctx, s.serveArgv()...)
	if err != nil {
		return fmt.Errorf("start %s: %w", s.Binary, err)
	}
	s.child = c
	s.lastStart = s.now()
	s.logf("daemon child started (pid %d)", c.Pid())
	return nil
}

// probe runs `status --json` once and returns its exit code.
func (s *Supervisor) probe(ctx context.Context) (int, error) {
	res, err := s.Runner.Run(ctx, dflt(s.StatusTimeout, DefaultStatusTimeout), s.statusArgv()...)
	if err != nil {
		return -1, err
	}
	return res.Exit, nil
}

// Sample reads the daemon's health for one slot. A child whose process has
// died is relaunched first (counted in Restarts), unless it was launched less
// than RestartBackoff ago or the supervisor was stopped. A sample taken while
// the process is dead is {down, -1, alive=false} and status is not run for it;
// otherwise status --json is probed and its exit code mapped: 0 healthy, 2
// degraded, anything else down. A degraded or down status never restarts the
// child, only a dead process does.
func (s *Supervisor) Sample(ctx context.Context) schema.DaemonSample {
	s.mu.Lock()
	child := s.child
	if child == nil || s.stopped {
		n := s.restarts
		s.mu.Unlock()
		return schema.DaemonSample{State: schema.DaemonDown, StatusExit: -1, Alive: false, Restarts: n}
	}
	select {
	case <-child.Done():
		s.logf("daemon child exited (code %d)", child.ExitCode())
		if s.now().Sub(s.lastStart) >= dflt(s.RestartBackoff, DefaultRestartBackoff) {
			if err := s.launchLocked(ctx); err != nil {
				s.logf("daemon restart failed: %v", err)
			} else {
				s.restarts++
				child = s.child
			}
		}
	default:
	}
	n := s.restarts
	s.mu.Unlock()

	select {
	case <-child.Done():
		return schema.DaemonSample{State: schema.DaemonDown, StatusExit: -1, Alive: false, Restarts: n}
	default:
	}
	code, err := s.probe(ctx)
	smp := schema.DaemonSample{StatusExit: code, Alive: true, Restarts: n}
	if err != nil {
		smp.StatusExit = -1
		s.logf("daemon status probe: %v", err)
	}
	switch {
	case err == nil && code == 0:
		smp.State = schema.DaemonHealthy
	case err == nil && code == 2:
		smp.State = schema.DaemonDegraded
	default:
		smp.State = schema.DaemonDown
	}
	return smp
}

// Stop ends the child cleanly (SIGTERM, then a kill after StopGrace). It is
// safe to call twice and when Start never succeeded.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	child := s.child
	s.child = nil
	s.stopped = true
	s.mu.Unlock()
	if child == nil {
		return nil
	}
	if err := child.Stop(dflt(s.StopGrace, DefaultStopGrace)); err != nil {
		s.logf("daemon stop: %v", err)
		return err
	}
	s.logf("daemon child stopped")
	return nil
}
