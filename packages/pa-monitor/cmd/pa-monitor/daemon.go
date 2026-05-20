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

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/poller"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	"github.com/phillipgreenii/pa-monitor/internal/daemon"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	signallayer "github.com/phillipgreenii/pa-monitor/internal/signal"
)

// runDaemon is invoked by the dispatcher when the user runs
// `pa-monitor daemon`. It owns the daemon process from start to
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

	runtimePath := filepath.Join(paths.Dir, "runtime.json")
	rs, rsErr := daemon.ReadRuntimeState(runtimePath)
	if rsErr != nil {
		fmt.Fprintf(os.Stderr, "daemon: read runtime state (continuing): %v\n", rsErr)
	}

	// Caffeinate manager — daemon owns its own Proc so the wrapper PID
	// is the daemon itself; agents are simply observed by the poller.
	caffProc := &caffeinate.Proc{}
	caffMgr := &caffeinate.Manager{
		Grace:   cfg.CaffeinateGrace,
		Spawn:   caffProc.Spawn,
		Kill:    caffProc.Kill,
		IsAlive: caffProc.IsAlive,
		Now:     time.Now,
		PID:     os.Getpid(),
	}
	// Ensure caffeinate is killed even if Run returns abnormally.
	defer func() { _ = caffProc.Kill() }()

	opts := daemon.RunOptions{
		Paths:               paths,
		Emitter:             emitter,
		Tick:                time.Duration(*tickS) * time.Second,
		PlanTier:            cfg.PlanTier,
		Caffeinate:          caffMgr,
		InitialCaffeinateOn: rs.CaffeinateOn,
		RuntimePath:         runtimePath,
	}

	if !*disablePoller {
		p, blockTr, weekTr, weeklyFn := buildPoller(cfg)
		opts.Poller = p
		opts.BlockTracker = blockTr
		opts.WeekTracker = weekTr
		opts.WeeklyFn = weeklyFn
		opts.WeeklyEvery = 12 // ~1 minute at 5s tick — weekly fetch is slow

		// Reuse the poller's signaler set for nudge dispatch.
		signalers := signallayer.DefaultSignalers()
		opts.NudgeFn = func(pid int, text string) error {
			sig := signallayer.ResolveSignaler(signalers, pid)
			if sig == nil {
				return fmt.Errorf("no signaler for pid %d", pid)
			}
			return sig.Send(pid, text)
		}
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
