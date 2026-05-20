package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/config"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/block"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/poller"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/week"
	"github.com/phillipgreenii/claude-agents-tui/internal/daemon"
	"github.com/phillipgreenii/claude-agents-tui/internal/otel"
	signallayer "github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

// runDaemon is invoked by the dispatcher when the user runs
// `claude-agents-tui daemon`. It owns the daemon process from start to
// clean shutdown.
func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	socketPath := fs.String("socket", "", "Override socket path (default: XDG-derived)")
	pidPath := fs.String("pidfile", "", "Override pidfile path (default: XDG-derived)")
	tickS := fs.Int("tick-seconds", 5, "Poll cadence in seconds")
	disablePoller := fs.Bool("no-poller", false, "Disable poller (state RPCs return empty)")
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

	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: config: %v\n", err)
		os.Exit(2)
	}

	emitter, err := otel.New(context.Background(), otel.Options{
		ServiceName:    "pa-monitor",
		ServiceVersion: version,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: otel init: %v\n", err)
		os.Exit(1)
	}

	// Load persisted runtime state (caffeinate toggle). Plan 3 client
	// surface lets users toggle; the apply step writes back here.
	runtimePath := filepath.Join(paths.Dir, "runtime.json")
	if rs, err := daemon.ReadRuntimeState(runtimePath); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: read runtime state (continuing): %v\n", err)
	} else {
		_ = rs // applied via Caffeinate RPC handler once wired
	}

	opts := daemon.RunOptions{
		Paths:    paths,
		Emitter:  emitter,
		Tick:     time.Duration(*tickS) * time.Second,
		PlanTier: cfg.PlanTier,
	}

	if !*disablePoller {
		p, blockTr, weekTr, weeklyFn := buildPoller(cfg)
		opts.Poller = p
		opts.BlockTracker = blockTr
		opts.WeekTracker = weekTr
		opts.WeeklyFn = weeklyFn
		opts.WeeklyEvery = 12 // ~1 minute at 5s tick — weekly fetch is slow
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := daemon.RunWith(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}
}

// buildPoller wires the same poller the TUI uses, but for the daemon
// process. ccusage runs are cached so the hot path stays cheap.
func buildPoller(cfg config.Config) (*poller.Poller, *block.Tracker, *week.Tracker, func(context.Context) (*ccusage.WeeklyEntry, error)) {
	home, _ := os.UserHomeDir()

	ccusageCache := ccusage.NewCachedRunner(60*time.Second, 60*time.Second,
		func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "ccusage", "blocks", "--active", "--json", "--offline").Output()
		})
	ccusageCache.Start(context.Background())

	prCache := session.NewPRCache(session.DefaultPRCachePath())
	signalers := signallayer.DefaultSignalers()

	p := &poller.Poller{
		SessionsDir:      session.DefaultSessionsDir(),
		ClaudeHome:       filepath.Join(home, ".claude"),
		PidAlive:         session.DefaultPidAlive,
		PlanTier:         cfg.PlanTier,
		WorkingThreshold: cfg.WorkingThreshold,
		IdleThreshold:    cfg.IdleThreshold,
		BurnWindowShort:  cfg.BurnWindowShort,
		BurnWindowLong:   cfg.BurnWindowLong,
		Now:              time.Now,
		CCUsageFn:        ccusageCache.Get,
		CCUsageStateFn:   func() (bool, error) { return ccusageCache.Probed(), ccusageCache.LastErr() },
		PRLookupFn:       prCache.Get,
		Signalers:        signalers,
	}

	blockCap := ccusage.PlanCapUSD(cfg.PlanTier)
	weekCap := ccusage.WeekCapUSD(cfg.PlanTier)
	blockTr := block.NewTracker(blockCap)
	weekTr := week.NewTracker(weekCap)

	weeklyRunner := &ccusage.Runner{}
	weeklyFn := func(ctx context.Context) (*ccusage.WeeklyEntry, error) {
		return weeklyRunner.CurrentWeekly(ctx)
	}

	return p, blockTr, weekTr, weeklyFn
}
