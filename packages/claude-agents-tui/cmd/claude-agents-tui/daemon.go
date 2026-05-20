package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/daemon"
	"github.com/phillipgreenii/claude-agents-tui/internal/otel"
)

// runDaemon is invoked by the dispatcher when the user runs
// `claude-agents-tui daemon`. It owns the daemon process from
// start to clean shutdown.
func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	socketPath := fs.String("socket", "", "Override socket path (default: XDG-derived)")
	pidPath := fs.String("pidfile", "", "Override pidfile path (default: XDG-derived)")
	tickS := fs.Int("tick-seconds", 5, "Poll cadence in seconds")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	paths, err := daemon.ResolvePaths(daemon.PathOverrides{
		Socket:  *socketPath,
		PIDFile: *pidPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: resolve paths: %v\n", err)
		os.Exit(1)
	}

	emitter, err := otel.New(context.Background(), otel.Options{
		ServiceName:    "pa-monitor",
		ServiceVersion: version,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: otel init: %v\n", err)
		os.Exit(1)
	}

	// Load any persisted runtime state. Plan 3 applies the caffeinate
	// toggle; for Plan 2 we only verify the file is read without error.
	runtimePath := filepath.Join(paths.Dir, "runtime.json")
	rs, rsErr := daemon.ReadRuntimeState(runtimePath)
	if rsErr != nil {
		fmt.Fprintf(os.Stderr, "daemon: read runtime state (continuing): %v\n", rsErr)
	}
	_ = rs

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := daemon.RunWith(ctx, daemon.RunOptions{
		Paths:   paths,
		Emitter: emitter,
		Tick:    time.Duration(*tickS) * time.Second,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}
}
