// Package daemon is the connector daemon kernel: the process-start and
// shutdown lifecycle every pg-connector daemon shares. A connector supplies a
// Spec and calls Run; the kernel owns
//
//   - the signal context and the run group (pkg/connectord/rungroup): the first
//     SIGINT or SIGTERM cancels the daemon's context and starts the single
//     shutdown deadline, a second signal takes its default action;
//   - the start protocol: take the lock and bind the socket (proc.StartCtx),
//     assemble the daemon, register its drain hooks in the order they must run,
//     serve the socket while startup runs concurrently, and only then report a
//     refusal;
//   - the opt-in pprof endpoint (Spec.PprofEnv), the status skeleton
//     (RunStatus), the JSONL log writer (jsonlsink) and the base telemetry
//     setup (otelsetup);
//   - the startup race: a shutdown signal that arrives while startup is still
//     running ends the serve cleanly (the drain answers it) instead of reporting
//     the startup failure the signal itself caused.
//
// A fix to shutdown order therefore lands here once. What the daemon does
// between "the lock is held" and "the drain is done" is the connector's: the
// Spec's Assemble builds it.
package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/x/exitcode"
	"github.com/phillipgreenii/x/lifecycle"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/rungroup"
)

const (
	// ExitOK is a clean drain.
	ExitOK = 0
	// ExitRefused is a serve that refused to start or failed while draining: the
	// lock is held, the socket path is too long, the state directory is unusable,
	// a startup step failed, a drain hook failed. It is exitcode.Unavailable.
	ExitRefused = exitcode.Unavailable
	// ExitUsage is a serve command line the daemon does not accept.
	ExitUsage = exitcode.Usage
)

// Hook is one drain step. Hooks run in the order Assemble returns them, after
// the in-flight handlers finish and before the lock is released.
type Hook struct {
	Name string
	Run  func(ctx context.Context) error
}

// Assembly is what a connector builds once it holds the lock.
type Assembly struct {
	// Handler serves one accepted connection (REQUIRED).
	Handler proc.Handler
	// Drain are the drain hooks, in the order they must run. They are registered
	// before startup begins and act on whatever startup got to.
	Drain []Hook
	// Start runs the startup steps while the socket is already being served and
	// returns the first failure; nil means the daemon is up. A nil Start means
	// there is nothing to start. Start gets the daemon's context; it ends on the
	// shutdown signal.
	Start func(ctx context.Context) error
}

// Spec is the connector's description of its daemon.
type Spec struct {
	// Name is the daemon's command name, as in "pg-connector-issue-jira serve",
	// the prefix of every refusal message (REQUIRED).
	Name string
	// Proc is the lock, socket and state-directory configuration. Its OnPhase is
	// set by Run (the run group's overrun report); Serve leaves it as given.
	Proc proc.Config
	// Assemble is called once, right after the lock is held, and builds the
	// daemon (REQUIRED).
	Assemble func() Assembly
	// Args are the arguments after the serve word (only --shutdown-deadline).
	// Run parses them; Serve ignores them.
	Args []string
	// PprofEnv names the environment variable that opts the serve mode into the
	// opt-in pprof endpoint (see StartPprof). Run reads the process environment
	// and starts it; Serve (the injected-context seam) never does. Empty means
	// the daemon has no endpoint.
	PprofEnv string
	// Stderr receives the reason a serve refused to start; nil discards it.
	Stderr io.Writer
	// WrapContext, when set, wraps the daemon's context before the daemon uses
	// it and returns the function that releases the wrapper (a test build's
	// parent watch). nil is the identity.
	WrapContext func(ctx context.Context) (context.Context, func())
}

func (s Spec) stderr() io.Writer {
	if s.Stderr == nil {
		return io.Discard
	}
	return s.Stderr
}

// Run runs the daemon as a process: it parses the serve arguments, puts the
// whole serve path (startup included) under the run group, and returns the
// process exit code. parent is the root context (context.Background in a real
// process); the run group adds the signal handling and the shutdown deadline.
func Run(parent context.Context, spec Spec) int {
	deadline, err := rungroup.ParseArgs(spec.Name+" serve", spec.Args)
	if err != nil {
		fmt.Fprintf(spec.stderr(), "%s serve: %v\n", spec.Name, err)
		return ExitUsage
	}
	if spec.PprofEnv != "" {
		defer StartPprof(spec.PprofEnv, os.Getenv, spec.stderr())()
	}
	return RunWithDeadline(parent, spec, deadline)
}

// RunWithDeadline is Run with the shutdown deadline already decided.
func RunWithDeadline(parent context.Context, spec Spec, deadline time.Duration) int {
	return rungroup.Run(parent, lifecycle.Options{
		Deadline: deadline,
		Log:      rungroup.Logger(spec.stderr()),
	}, func(ctx context.Context, setPhase func(string)) int {
		spec.Proc.OnPhase = setPhase
		return Serve(ctx, spec)
	})
}

// Serve is the daemon with its shutdown context injected (the seam in-process
// tests use); it runs the start protocol until ctx ends and returns the exit
// code. The run group is the caller's.
func Serve(ctx context.Context, spec Spec) int {
	stderr := spec.stderr()
	if spec.WrapContext != nil {
		var release func()
		ctx, release = spec.WrapContext(ctx)
		defer release()
	}

	srv, err := proc.StartCtx(ctx, spec.Proc)
	if err != nil {
		fmt.Fprintf(stderr, "%s serve: %v\n", spec.Name, err)
		return ExitRefused
	}

	asm := spec.Assemble()
	// The hooks are registered now, in the order they must run, and act on
	// whatever startup got to.
	drain := &proc.Hooks{}
	for _, h := range asm.Drain {
		drain.Register(h.Name, h.Run)
	}

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx, asm.Handler, drain) }()

	if asm.Start != nil {
		if err := asm.Start(ctx); err != nil {
			stopServe()
			<-served
			if ctx.Err() != nil {
				// The shutdown signal arrived during startup: whatever startup was
				// doing failed because of it, and the drain above is the answer.
				return ExitOK
			}
			fmt.Fprintf(stderr, "%s serve: %v\n", spec.Name, err)
			return ExitRefused
		}
	}

	if err := <-served; err != nil {
		fmt.Fprintf(stderr, "%s serve: %v\n", spec.Name, err)
		return ExitRefused
	}
	return ExitOK
}
