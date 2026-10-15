package main

import (
	"context"
	"flag"
	"io"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/anchor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// runAnchor is the autonomous watchdog the keeper's caffeinate follows. It
// takes every parameter from argv, never reads config.toml and keeps no
// connection to the keeper. A missing or malformed flag is a usage error
// (exit 2) that writes nothing. After writing anchor-exit.json it exits 0.
func runAnchor(args []string, _, stderr io.Writer) int {
	set := newFlagSet("anchor", stderr)
	release := set.Int("release-pct", -1, "exit when on battery at or below this percent (0: never)")
	blind := set.Int("blind-pct", -1, "exit when unreadable and the last good reading was on battery at or below this percent (0: off)")
	watchdogStr := set.String("watchdog", "", "exit when the heartbeat mtime stays unchanged this long, in awake time (for example 90s)")
	heartbeat := set.String("heartbeat", "", "the keeper's heartbeat file")
	intervalStr := set.String("interval", "", "polling period (the keeper's heartbeat interval, for example 30s)")
	stateDir := set.String("state-dir", "", "the state directory anchor-exit.json is written under")
	if exit, done := parseFlags(set, args); done {
		return exit
	}
	if set.NArg() != 0 {
		return usageError(stderr, "anchor: unexpected argument %q", set.Arg(0))
	}
	seen := map[string]bool{}
	set.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	for _, name := range []string{"release-pct", "blind-pct", "watchdog", "heartbeat", "interval", "state-dir"} {
		if !seen[name] {
			return usageError(stderr, "anchor: --%s is required", name)
		}
	}
	p := anchor.Params{ReleasePct: *release, BlindPct: *blind, Heartbeat: *heartbeat}
	var err error
	if p.Watchdog, err = parseDuration(*watchdogStr); err != nil {
		return usageError(stderr, "anchor: --watchdog: %v", err)
	}
	if p.Interval, err = parseDuration(*intervalStr); err != nil {
		return usageError(stderr, "anchor: --interval: %v", err)
	}
	if err := p.Validate(); err != nil {
		return usageError(stderr, "anchor: %v", err)
	}
	if *stateDir == "" {
		return usageError(stderr, "anchor: --state-dir must not be empty")
	}
	d, err := lease.CheckDir(*stateDir)
	if err != nil {
		return failState(stderr, "anchor", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if _, err := anchor.Run(ctx, p, anchor.Env{Dir: d}); err != nil {
		if ctx.Err() != nil {
			return exitOK // stopped by a signal: leave no file, as a kill would
		}
		return failState(stderr, "anchor", err)
	}
	return exitOK
}
