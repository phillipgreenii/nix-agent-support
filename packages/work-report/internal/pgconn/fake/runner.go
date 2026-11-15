package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
)

// noRouteExit is the exit status of a call that matches no route.
const noRouteExit = 64

// Runner is Install without the process: the same routes and the same
// matching (argv prefix, first match wins, exit 64 when none matches), served
// in-process as a pgconn.DetailedRunner. A test that does not need the real
// exec path uses it so each call costs no process launch.
type Runner struct {
	routes []Route

	mu    sync.Mutex
	calls [][]string
}

// NewRunner returns an in-process fake implementing routes.
func NewRunner(routes ...Route) *Runner { return &Runner{routes: routes} }

// Run implements pgconn.Runner.
func (r *Runner) Run(ctx context.Context, args ...string) ([]byte, int, error) {
	out, err := r.RunDetailed(ctx, args...)
	return out.Stdout, out.ExitCode, err
}

// RunDetailed implements pgconn.DetailedRunner. Like the exec runner it
// reports a done context as the error and a non-zero exit as data.
func (r *Runner) RunDetailed(ctx context.Context, args ...string) (pgconn.Output, error) {
	if err := ctx.Err(); err != nil {
		return pgconn.Output{}, err
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	r.mu.Unlock()
	for _, rt := range r.routes {
		if len(args) < len(rt.Match) || !slices.Equal(args[:len(rt.Match)], rt.Match) {
			continue
		}
		out := pgconn.Output{Stdout: []byte(rt.Stdout), ExitCode: rt.Exit}
		if rt.Exit != 0 {
			out.Stderr = []byte(fmt.Sprintf("fake pg-connector: simulated failure (exit %d)\n", rt.Exit))
		}
		return out, nil
	}
	return pgconn.Output{Stderr: []byte("fake pg-connector: no route for this call\n"), ExitCode: noRouteExit}, nil
}

// Calls returns every argv (without the program name) received, in order.
func (r *Runner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Switch is a pgconn.DetailedRunner for a test package to hand to the code
// under test once: it forwards to the in-process fake of the latest Use, and
// to the real exec runner (pg-connector from PATH, so fake.Install still
// works) when no Use is active.
type Switch struct {
	mu  sync.Mutex
	cur *Runner
}

// Use makes the Switch serve routes in-process until the test ends. Tests
// sharing a Switch MUST NOT run in parallel.
func (s *Switch) Use(t *testing.T, routes ...Route) *Runner {
	t.Helper()
	r := NewRunner(routes...)
	s.mu.Lock()
	s.cur = r
	s.mu.Unlock()
	t.Cleanup(func() {
		s.mu.Lock()
		s.cur = nil
		s.mu.Unlock()
	})
	return r
}

func (s *Switch) target() pgconn.DetailedRunner {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		return s.cur
	}
	return pgconn.NewExec().(pgconn.DetailedRunner)
}

// Run implements pgconn.Runner.
func (s *Switch) Run(ctx context.Context, args ...string) ([]byte, int, error) {
	return s.target().Run(ctx, args...)
}

// RunDetailed implements pgconn.DetailedRunner.
func (s *Switch) RunDetailed(ctx context.Context, args ...string) (pgconn.Output, error) {
	return s.target().RunDetailed(ctx, args...)
}
