package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
)

// exitServe is the exit status of a serve that refused to start or failed
// while draining (lock held, socket path too long, an unusable state
// directory, a store newer than this binary, a failed drain hook).
const exitServe = serve.ExitRefused

// serveHooks is a build-time seam (-ldflags "-X main.serveHooks=viewer_interval=300ms"),
// never set in a shipped build: it names the timing seams of the daemon's
// assembly (see serve.ParseHooks) so a real-process test need not wait out the
// production intervals. Like clientBudgetOverride it is a link-time string, not
// an environment variable: the binary reads no environment but the config's
// five variables and the one operator-diagnostics variable PG_CONNECTOR_GITHUB_PPROF_ADDR
// (pprof.go), which opens no listener unless set and never changes a setting.
var serveHooks = ""

// runServe runs the daemon until SIGTERM or SIGINT, then drains. It returns 0
// after a clean drain.
func runServe(cfg *config.Config, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Opt-in profiling endpoint (PG_CONNECTOR_GITHUB_PPROF_ADDR): the serve mode
	// only, never pr, ci or status. It reports on stderr and a rejected value
	// never stops the daemon. runServe is the real-process entry, so it reads the
	// process environment directly; serveWith (the injected-context seam used by
	// tests) does not start it.
	defer startPprofFromEnv(os.Getenv, stderr)()
	return serveWith(ctx, cfg, stderr)
}

// serveWith is runServe with the shutdown context injected.
func serveWith(ctx context.Context, cfg *config.Config, stderr io.Writer) int {
	hooks, err := serve.ParseHooks(serveHooks)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-github serve: %v\n", err)
		return exitServe
	}
	return serve.Run(ctx, serve.Options{Config: cfg, Version: Version, Stderr: stderr, Hooks: hooks})
}
