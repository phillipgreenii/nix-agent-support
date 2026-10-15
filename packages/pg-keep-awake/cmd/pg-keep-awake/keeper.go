package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/keeper"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/telemetry"
)

// runKeeper is the daemon launchd keeps alive. It takes no flags: the state
// and config locations come from XDG_STATE_HOME and XDG_CONFIG_HOME. It takes
// the singleton keeper.lock (a second keeper in the same state directory
// exits at once with a stderr line), creates keeper.heartbeat first, loads
// the configuration at startup only (falling back to config.last-good.toml and
// then the defaults on a bad file; it never crash-loops on one), adopts a
// running pair and ticks until SIGTERM, when it runs the graceful-shutdown
// sweep and exits 0.
func runKeeper(args []string, _, stderr io.Writer) int {
	set := newFlagSet("keeper", stderr)
	if exit, done := parseFlags(set, args); done {
		return exit
	}
	if set.NArg() != 0 {
		return usageError(stderr, "keeper: unexpected argument %q", set.Arg(0))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	d, err := ensureStateDir()
	if err != nil {
		return failState(stderr, "keeper", err)
	}
	lock, err := keeper.TryLockInstance(d)
	if err != nil {
		fmt.Fprintf(stderr, "pg-keep-awake: keeper: %v\n", err)
		if errors.Is(err, keeper.ErrAlreadyRunning) {
			return exitUnexpected // a second keeper exits at once
		}
		return failState(stderr, "keeper", err)
	}
	defer func() { _ = lock.Release() }()

	// The heartbeat is the first file the keeper makes (adoption step 1).
	if err := keeper.TouchHeartbeat(d, time.Now()); err != nil {
		return failState(stderr, "keeper", fmt.Errorf("create keeper.heartbeat: %w", err))
	}

	cfg, cfgErr := loadKeeperConfig(d, stderr)

	emitter, err := telemetry.New(ctx, telemetry.Options{
		Endpoint:       cfg.OTel.Endpoint,
		ServiceName:    "pg-keep-awake",
		ServiceVersion: Version,
	})
	if err != nil {
		// Telemetry is never a reason to stop holding the assertion; New
		// returned the no-op emitter.
		fmt.Fprintf(stderr, "pg-keep-awake: keeper: telemetry disabled: %v\n", err)
	}

	k := keeper.NewKeeper(keeper.KeeperOptions{
		Dir:         d,
		Config:      cfg,
		ConfigError: cfgErr,
		Version:     Version,
		Emitter:     emitter,
		Log:         stderr,
	})
	if err := k.Run(ctx); err != nil {
		return failState(stderr, "keeper", err)
	}
	return exitOK
}

// loadKeeperConfig resolves and loads the configuration with the fallback
// chain. It never fails; the second return is the error text to report as
// keeper.config_error ("" when the file parsed or is absent).
func loadKeeperConfig(d *lease.Dir, stderr io.Writer) (config.Config, string) {
	path, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(stderr, "pg-keep-awake: keeper: %v; using the built-in defaults\n", err)
		return config.Defaults().Effective(), err.Error()
	}
	cfg, errText, warn := keeper.LoadConfig(d, path)
	for _, w := range warn {
		fmt.Fprintf(stderr, "pg-keep-awake: keeper: %v\n", w)
	}
	return cfg, errText
}
