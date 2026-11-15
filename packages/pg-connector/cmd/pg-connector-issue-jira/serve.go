package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/parentwatch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/rungroup"
	"github.com/phillipgreenii/x/lifecycle"
)

// exitServe is the exit status of a serve that refused to start or failed
// while draining (lock held, socket path too long, an unusable state
// directory, a store newer than this binary, a failed drain hook).
const exitServe = serve.ExitRefused

// serveHooks is a build-time seam (-ldflags "-X main.serveHooks=poll_interval=50ms"),
// never set in a shipped build: it names the timing seams of the daemon's
// assembly (see serve.ParseHooks) so a real-process test need not wait out the
// production intervals. Like the GitHub daemon's it is a link-time string, not
// an environment variable: the daemon reads no environment variable but the
// operator-diagnostics variable PG_CONNECTOR_ISSUE_JIRA_PPROF_ADDR (pprof.go),
// which opens no listener unless set and never changes a setting.
var serveHooks = ""

// runServe runs the daemon until SIGTERM or SIGINT, then drains. It returns 0
// after a clean drain. The whole serve path, startup included, runs as one loop
// of an x/lifecycle group: the first signal cancels it and starts the one
// shutdown deadline (--shutdown-deadline, owned by the nix module, which derives
// launchd's ExitTimeOut from it); past the deadline the process exits 10, and a
// second signal takes its default action.
func runServe(cfg *config.Config, args []string, stderr io.Writer) int {
	deadline, err := rungroup.ParseArgs("pg-connector-issue-jira serve", args)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira serve: %v\n", err)
		return exitUsage
	}
	// Opt-in profiling endpoint (see pprof.go): the serve mode only. It reports
	// on stderr and a rejected value never stops the daemon. runServe is the
	// real-process entry, so it reads the process environment directly;
	// serveWith (the injected-context seam used by tests) does not start it.
	defer startPprofFromEnv(os.Getenv, stderr)()
	return rungroup.Run(context.Background(), lifecycle.Options{
		Deadline: deadline,
		Log:      rungroup.Logger(stderr),
	}, func(ctx context.Context, setPhase func(string)) int {
		return serveWith(ctx, cfg, stderr, setPhase)
	})
}

// serveWith is the daemon with the shutdown context injected; setPhase (nil
// allowed) names the drain step in progress for the run group's overrun report.
func serveWith(ctx context.Context, cfg *config.Config, stderr io.Writer, setPhase func(string)) int {
	hooks, err := serve.ParseHooks(serveHooks)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira serve: %v\n", err)
		return exitServe
	}
	// Test builds only (hooks.ParentWatch): drain when the parent ends, so a
	// daemon whose test process was killed without running its cleanups does not
	// outlive it (pg2-krpw80, as pg2-qupkv0 for the GitHub daemon). A zero
	// ParentWatch (every shipped build) is inert.
	ctx, stopWatch := parentwatch.ExitWithParent(ctx, hooks.ParentWatch, os.Getppid)
	defer stopWatch()
	return serve.Run(ctx, serve.Options{Config: cfg, Version: Version, Stderr: stderr, Hooks: hooks, OnPhase: setPhase})
}
