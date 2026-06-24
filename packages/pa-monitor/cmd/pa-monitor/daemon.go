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

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/poller"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	"github.com/phillipgreenii/pa-monitor/internal/daemon"
	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/pa-monitor/internal/labels/detectors"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	signallayer "github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	opts, cleanup, err := buildRunOptions(ctx, cfg, paths, version,
		time.Duration(*tickS)*time.Second, *disablePoller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: build run options: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	// Startup health check: tmux/cmux are required, not optional. Each
	// signaler shells out to its multiplexer's CLI; a missing binary (e.g.
	// absent from the launchd PATH) otherwise fails silently — the terminal
	// classifies as "unknown" and auto-resume nudges are dropped with no
	// signal. Report loudly (stderr + metric) but keep running so billing /
	// context / other-multiplexer monitoring stays alive.
	for _, mb := range signallayer.MissingBinaries(signallayer.DefaultSignalers(), exec.LookPath) {
		fmt.Fprintf(os.Stderr,
			"daemon: ERROR: signaler %q requires %q which is not on PATH; "+
				"detection and auto-resume for %s sessions are disabled until it is installed\n",
			mb.Signaler, mb.Binary, mb.Signaler)
		opts.Emitter.RecordSignalerBinaryMissing(map[string]string{
			"signaler": mb.Signaler,
			"binary":   mb.Binary,
		})
	}

	if err := daemon.RunWith(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}
}

// buildRunOptions assembles the daemon's RunOptions exactly the way
// runDaemon does — same store wiring, same WriteService / ReadService
// construction, same fields — but stops short of calling daemon.RunWith.
// The returned cleanup function releases the WriteService goroutine,
// closes the otel emitter, and kills any caffeinate subprocess; callers
// must always invoke it.
//
// runDaemon uses this in production; daemon_test.go uses it for a smoke
// test that asserts the required RunOptions fields (WriteService,
// ReadService, SessionsDir) are wired. Two regressions shipped where
// runDaemon forgot to set one of these — keeping the assembly in a
// testable helper catches that class of bug at test time.
func buildRunOptions(ctx context.Context, cfg config.Config, paths daemon.Paths, ver string, tick time.Duration, disablePoller bool) (daemon.RunOptions, func(), error) {
	// Accumulate cleanup steps; run them in reverse order on success or
	// on error from anywhere in this function.
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(err error) (daemon.RunOptions, func(), error) {
		cleanup()
		return daemon.RunOptions{}, func() {}, err
	}

	// Source OTel config from the shared config file (single source of truth
	// for daemon + bridge + tui). Only-if-unset, so an explicit env still wins.
	config.ApplyOTelEnv(cfg.OTel)

	emitter, err := otel.New(ctx, otel.Options{
		ServiceName:    "pa-monitor",
		ServiceVersion: ver,
	})
	if err != nil {
		return fail(fmt.Errorf("otel init: %w", err))
	}

	runtimePath := filepath.Join(paths.Dir, "runtime.json")

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
	cleanups = append(cleanups, func() { _ = caffProc.Kill() })

	// cmux-bridge registry: tracks bridges that have called RegisterBridge,
	// so the poller can refine "cmux" terminal-host labels into "cmux" /
	// "cmux (no bridge)" / "cmux (bridge disconnected)". staleAfter of 30s
	// gives bridges ~3 of their default 10s heartbeats before being marked
	// stale; tune via the bridge's heartbeatInterval if you change it.
	bridgeRegistry := bridge.NewRegistry(30 * time.Second)
	// CmuxAncestor for the RegisterBridge handler. Fishes the package
	// singleton CmuxSignaler out of DefaultSignalers() so the daemon's
	// signal slice and the registry handler share a single ps-cache.
	var cmuxAncestor func(int) (int, bool)
	for _, sig := range signallayer.DefaultSignalers() {
		if cs, ok := sig.(*signallayer.CmuxSignaler); ok {
			cmuxAncestor = cs.FindCmuxServerAncestor
			break
		}
	}

	opts := daemon.RunOptions{
		Paths:               paths,
		Emitter:             emitter,
		Tick:                tick,
		PlanTier:            cfg.PlanTier,
		Caffeinate:          caffMgr,
		InitialCaffeinateOn: false, // updated from DB below when available
		RuntimePath:         runtimePath,
		Version:             ver,
		BridgeRegistry:      bridgeRegistry,
		CmuxAncestor:        cmuxAncestor,
		Detectors: []labels.Detector{
			detectors.DefaultScope{}, // sets workspace.scope=personal; Gascity overrides for GC sessions
			detectors.Terminal{},
			detectors.Gascity{},
			detectors.Repo{},
			detectors.Project{},
			detectors.Agent{},
		},
		Decorators: buildDecorators(cfg.Decorators),
	}

	if !disablePoller {
		p, blockTr, weekTr, weeklyFn := buildPoller(ctx, cfg)
		p.BridgeRegistry = bridgeRegistry

		// Open the SQLite database and wire WriteService into the poller so
		// that contribution rows are persisted on every tick. The DB lives at
		// <XDG state dir>/pa-monitor/state.db — the same directory the daemon
		// already uses for its pidfile and socket.
		dbPath := filepath.Join(paths.Dir, "state.db")
		if db, err := sqlite.Open(dbPath); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: sqlite open %s: %v (continuing without DB)\n", dbPath, err)
		} else if err := sqlite.Migrate(context.Background(), db); err != nil {
			_ = db.Close()
			fmt.Fprintf(os.Stderr, "daemon: sqlite migrate: %v (continuing without DB)\n", err)
		} else {
			ws := service.NewWriteService(service.WriteDeps{
				Sessions:      sqlite.NewSessionStore(db),
				Blocks:        sqlite.NewBlockStore(db),
				Weeks:         sqlite.NewWeekStore(db),
				Contributions: sqlite.NewContributionStore(db),
				Toggles:       sqlite.NewToggleStore(db),
				Nudges:        sqlite.NewNudgeStore(db),
			})
			ws.Start(context.Background())
			// Ensure the write goroutine is stopped on daemon exit.
			cleanups = append(cleanups, ws.Stop)

			// ReadService materialises the aggregate.Tree from DB queries on
			// every snapshot. Without this, sharedState.snapshot() returns nil
			// and all gRPC clients see an empty DaemonState (CLI: 0 sessions,
			// TUI: 'loading…' forever).
			rs := service.NewReadService(service.ReadDeps{
				Sessions: sqlite.NewSessionStore(db),
				Blocks:   sqlite.NewBlockStore(db),
				Weeks:    sqlite.NewWeekStore(db),
				Toggles:  sqlite.NewToggleStore(db),
				Nudges:   sqlite.NewNudgeStore(db),
			})

			// Wire into both the poller (per-session upserts + contributions)
			// and RunOptions (block / week upserts in lifecycle.go, read
			// materialisation in sharedState).
			p.WriteService = ws
			p.DB = db
			opts.WriteService = ws
			opts.ReadService = rs

			// Read the persisted toggles from the DB (primary source of truth
			// since the runtime.json -> SQLite migration). Both seed the live
			// daemon state at startup so user toggles survive restarts.
			toggles := sqlite.NewToggleStore(db)
			if v, ok, err := toggles.Get(context.Background(), "caffeinate_on"); err == nil && ok {
				opts.InitialCaffeinateOn = v
			}
			if v, ok, err := toggles.Get(context.Background(), "auto_resume_enabled"); err == nil && ok {
				opts.InitialAutoResumeEnabled = v
			}
		}

		opts.Poller = p
		opts.BlockTracker = blockTr
		opts.WeekTracker = weekTr
		opts.WeeklyFn = weeklyFn
		opts.SessionsDir = p.SessionsDir
		opts.WeeklyEvery = 12 // ~1 minute at 5s tick — weekly fetch is slow

		// Without NudgerSignalers being non-empty, lifecycle.go skips
		// constructing the WatermarkStore — which makes SetAutoResume,
		// NudgeQueue, and NudgeCancel all return FailedPrecondition. The
		// integration test TestRunWith_SetAutoResumePersistsViaGetState
		// guards this contract.
		opts.NudgerSignalers = signallayer.DefaultSignalers()
		opts.AutoResumeMessage = cfg.AutoResumeMessage
		opts.AutoResumeDelay = cfg.AutoResumeDelay
		opts.DisruptGrace = cfg.DisruptGrace
		opts.EscalationAfter = cfg.EscalationAfter
	}

	return opts, cleanup, nil
}

// buildDecorators translates the user's [[decorator]] config blocks into
// the labels.Decorator runners the lifecycle merges per-session. A bad
// entry (e.g. a Command outside /nix/store/) logs and is skipped so a
// typo in one decorator doesn't sink the daemon — same swallow-and-warn
// posture the runner itself takes for runtime failures.
func buildDecorators(cfgs []config.DecoratorConfig) []*labels.Decorator {
	out := make([]*labels.Decorator, 0, len(cfgs))
	for _, c := range cfgs {
		dec, err := labels.NewDecorator(labels.DecoratorConfig{
			Name:      c.Name,
			Command:   c.Command,
			TimeoutMS: c.TimeoutMS,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: skipping decorator %q: %v\n", c.Name, err)
			continue
		}
		out = append(out, dec)
	}
	return out
}

// buildPoller wires the same poller the TUI uses, but for the daemon
// process. ccusage runs are cached so the hot path stays cheap. The ctx
// controls the lifetime of the background ccusage refresh goroutine —
// pass the daemon's signal-bound ctx in production so the goroutine
// exits on SIGTERM; tests pass a short-lived ctx to avoid leaks.
func buildPoller(ctx context.Context, cfg config.Config) (*poller.Poller, *block.Tracker, *week.Tracker, func(context.Context) (*ccusage.WeeklyEntry, error)) {
	home, _ := os.UserHomeDir()

	ccusageCache := ccusage.NewCachedRunner(60*time.Second, 60*time.Second,
		func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "ccusage", "blocks", "--active", "--json", "--offline").Output()
		})
	ccusageCache.Start(ctx)

	prCache := session.NewPRCache(session.DefaultPRCachePath())
	signalers := signallayer.DefaultSignalers()

	p := &poller.Poller{
		SessionsDir:        session.DefaultSessionsDir(),
		ClaudeHome:         filepath.Join(home, ".claude"),
		PidAlive:           session.DefaultPidAlive,
		PlanTier:           cfg.PlanTier,
		WorkingThreshold:   cfg.WorkingThreshold,
		IdleThreshold:      cfg.IdleThreshold,
		WaitingFreshWindow: cfg.WaitingFreshWindow,
		BurnWindowShort:    cfg.BurnWindowShort,
		BurnWindowLong:     cfg.BurnWindowLong,
		Now:                time.Now,
		CCUsageFn:          ccusageCache.Get,
		CCUsageStateFn:     func() (bool, error) { return ccusageCache.Probed(), ccusageCache.LastErr() },
		PRLookupFn:         prCache.Get,
		Signalers:          signalers,
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
