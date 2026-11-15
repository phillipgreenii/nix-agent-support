// Package rungroup is the process-level run group of a pg-connector daemon: it
// puts the daemon's whole serve path (startup included) under one x/lifecycle
// group, so the signal context, the single hard shutdown deadline and the
// second-signal default action are the same for every daemon.
//
// The deadline is owned by the deployment: the nix module passes it as
// --shutdown-deadline and derives launchd's ExitTimeOut from the same number.
// The group's clock starts when its context ends, whatever the daemon was
// doing, so a SIGTERM that lands during startup (store open, migrations) is
// bounded exactly like one that lands while serving.
package rungroup

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/phillipgreenii/x/lifecycle"
)

const (
	// DefaultDeadline is the shutdown deadline when --shutdown-deadline is
	// absent. The nix module always passes it, so this only serves a daemon
	// started by hand.
	DefaultDeadline = 40 * time.Second
	// ExitOverrun is the process status of a hard exit at the deadline.
	ExitOverrun = 10
	// FlagName is the daemon flag that carries the deadline.
	FlagName = "shutdown-deadline"
)

// ParseArgs parses the arguments after the serve word. The only flag is
// --shutdown-deadline; anything else is an error.
func ParseArgs(name string, args []string) (time.Duration, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	d := lifecycle.DeadlineFlag(fs, FlagName, DefaultDeadline)
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	if fs.NArg() > 0 {
		return 0, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *d <= 0 {
		return 0, fmt.Errorf("--%s must be positive, got %s", FlagName, *d)
	}
	return *d, nil
}

// Run runs fn as the group's only loop and returns its result once the group
// has finished. fn gets the group's context (it ends on the first SIGINT or
// SIGTERM) and a function naming the shutdown step in progress. opt.Deadline
// and opt.Log are the caller's; opt.ExitCode defaults to ExitOverrun.
func Run(parent context.Context, opt lifecycle.Options, fn func(ctx context.Context, setPhase func(string)) int) int {
	if opt.ExitCode == 0 {
		opt.ExitCode = ExitOverrun
	}
	g := lifecycle.New(parent, opt)
	var code int
	g.Go("daemon", func(ctx context.Context) error {
		code = fn(ctx, g.SetPhase)
		return nil
	})
	_ = g.Run()
	return code
}

// Logger is the raw logger the group reports to: a text handler straight on w,
// with no lock a stuck loop could hold.
func Logger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}
