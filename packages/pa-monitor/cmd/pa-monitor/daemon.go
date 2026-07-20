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
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/poller"
	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	"github.com/phillipgreenii/pa-monitor/internal/daemon"
	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/pa-monitor/internal/labels/detectors"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	signallayer "github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
	"github.com/phillipgreenii/pa-monitor/internal/timing"
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

	// Account carries the plan identity and pricing inputs (the per-block /
	// per-week caps). Built once here and threaded to the poller, trackers, and
	// the store-conversion path so no consumer looks caps up from usage.
	acct := account.LoadAccount(cfg)

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

	// Connection-timing windows, DERIVED from the two base cadences in config
	// (internal/timing). StaleAfter is guaranteed >= 3x the heartbeat interval
	// and PushBudget > 2x the snapshot interval, so the daemon's reaper window
	// and the bridge's watchdog cannot be configured into an inversion. The
	// bridge derives the very same values from the same config, so the two
	// processes stay consistent.
	timings := timing.Derive(timing.Config{
		SnapshotInterval:  cfg.BridgeSnapshotInterval,
		HeartbeatInterval: cfg.BridgeHeartbeatInterval,
	})
	// cmux-bridge registry: tracks bridges that have called RegisterBridge,
	// so the poller can refine "cmux" terminal-host labels into "cmux" /
	// "cmux (no bridge)" / "cmux (bridge disconnected)". staleAfter gives a
	// bridge >= 3 of its heartbeats before being marked stale (see timing pkg).
	bridgeRegistry := bridge.NewRegistry(timings.StaleAfter)
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

	// Provider cache: the shared owner of point-in-time PULL lookups (git-branch,
	// subshell, terminal-host, env, repo-label, PR). Created at the composition
	// root so BOTH the poller (buildPoller wires its prCache-backed PR + the
	// Discoverer's env) and the label pipeline's Repo detector share one cache and
	// one eviction lifecycle. The terminal-host + repo-label fetch boundaries are
	// wired here; the PR + env boundaries are wired in buildPoller.
	providerCache := provider.New(time.Now)
	providerCache.PidAlive = session.DefaultPidAlive
	providerCache.FetchTerminalHost = func(pid int) string {
		return signallayer.DetectHost(signallayer.DefaultSignalers(), pid)
	}
	providerCache.FetchRepoLabel = detectors.RepoLabelFor

	opts := daemon.RunOptions{
		Paths:               paths,
		Emitter:             emitter,
		Tick:                tick,
		PlanTier:            cfg.PlanTier,
		Account:             acct,
		Caffeinate:          caffMgr,
		InitialCaffeinateOn: false, // updated from DB below when available
		RuntimePath:         runtimePath,
		Version:             ver,
		BridgeRegistry:      bridgeRegistry,
		CmuxAncestor:        cmuxAncestor,
		// Repo-label source defaults to the shared provider cache; the poller
		// branch below overrides it to poller.RepoLabelSource so the tick reads
		// the producer-published repo-label map instead of the shared Cache (C1).
		Detectors:  defaultDetectors(providerCache),
		Decorators: buildDecorators(cfg.Decorators),
		// Re-read the config each tick and rebuild the decorator pipeline on
		// change, so a decorator written by `pn workspace apply` after the
		// daemon booted is picked up without a manual restart (pg2-r1f1j.8).
		ConfigPath: config.DefaultPath(),
		ReloadDecorators: func() ([]labels.FailableDetector, error) {
			c, err := config.Load(config.DefaultPath())
			if err != nil {
				return nil, err
			}
			return labels.AsFailable(buildDecorators(c.Decorators)), nil
		},
		BridgeSnapshotInterval: timings.SnapshotInterval,
		BridgeStaleAfter:       timings.StaleAfter,
	}

	if !disablePoller {
		p, blockTr, weekTr := buildPoller(ctx, cfg, acct, providerCache)
		p.BridgeRegistry = bridgeRegistry

		// C1: the tick's repo-label source is the producer-published DerivedState
		// map (atomic Load), NOT the shared provider Cache — so the label pipeline
		// cannot race the producer's cwd-node eviction. The producer computes the
		// label (owning the Cache) in Assemble and publishes it.
		opts.Detectors = defaultDetectors(poller.RepoLabelSource{Prod: p.Producer()})

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
			opts.DB = db
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
		opts.SessionsDir = p.SessionsDir
		// WeeklyEvery gates how often the daemon reads the (folded) weekly cost
		// projection + upserts it: ~1/12 ticks (~1 minute at 5s), preserving the
		// pre-fold cadence. The value itself comes from the corpus Monitor's
		// UsagePricing observer; opts.WeeklyFn / opts.Limits are no longer wired in
		// production (removed with the inline path in pg2-66h9g — limits + weekly are
		// read from the Monitor). They remain on RunOptions only as the fallback for
		// non-Monitor test pollers.
		opts.WeeklyEvery = 12

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

// defaultDetectors builds the daemon's built-in label detector set. repoCache is
// the workspace.repo source for the Repo detector: the shared provider Cache in
// the no-poller fallback, or poller.RepoLabelSource (the producer-published map)
// on the live path (C1). Both satisfy the Repo detector's Cache interface.
func defaultDetectors(repoCache interface {
	RepoLabel(cwd string) (string, bool)
},
) []labels.Detector {
	return []labels.Detector{
		detectors.DefaultScope{}, // sets workspace.scope=personal; decorators may override
		detectors.Terminal{},
		detectors.Repo{Cache: repoCache},
		detectors.Project{},
		detectors.Agent{},
	}
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
			Env:       c.Env,
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

// buildPoller wires the same poller the TUI uses, but for the daemon process.
// The CostPricer is the native adapter (ADR 0021 §3/Phase 4): it computes the
// active 5h block and weekly cost from local transcripts × the Account's
// per-model price table, with no ccusage subprocess. It scans on demand, so no
// background goroutine or ctx lifetime is involved. Building it here — the
// composition root — is where the concrete adapter is named; the poller and
// daemon see only the CostPricer port.
func buildPoller(_ context.Context, cfg config.Config, acct account.Account, cache *provider.Cache) (*poller.Poller, *block.Tracker, *week.Tracker) {
	home, _ := os.UserHomeDir()
	claudeHome := filepath.Join(home, ".claude")

	// Bound the file-backed PR cache (15m found-TTL) and time the gh spawn inside
	// LookupFn so the pr_lookup subprocess metric fires ONLY on a real fetch
	// (miss/expiry), not on a cache hit — the single-cache, no-double-caching PR
	// path. The provider records the live (cwd,branch) keys + drives Prune.
	prCache := session.NewPRCache(session.DefaultPRCachePath())
	prCache.FoundTTL = 15 * time.Minute
	prCache.LookupFn = func(ctx context.Context, cwd, branch string) (session.PRInfo, bool, error) {
		start := time.Now()
		info, found, err := session.LookupPR(ctx, cwd, branch)
		cache.Record("pr_lookup", time.Since(start))
		return info, found, err
	}
	cache.PRBackend = prCache.Get
	cache.PRPrune = prCache.Prune

	p := &poller.Poller{
		SessionsDir:        session.DefaultSessionsDir(),
		ClaudeHome:         claudeHome,
		PidAlive:           session.DefaultPidAlive,
		PlanTier:           cfg.PlanTier,
		BlockCapUSD:        acct.BlockCap(),
		WorkingThreshold:   cfg.WorkingThreshold,
		IdleThreshold:      cfg.IdleThreshold,
		WaitingFreshWindow: cfg.WaitingFreshWindow,
		BurnWindowShort:    cfg.BurnWindowShort,
		BurnWindowLong:     cfg.BurnWindowLong,
		Now:                time.Now,
		Signalers:          signallayer.DefaultSignalers(),
		Providers:          cache,
	}

	// Corpus Monitor: the single owner of the corpus read — discovery, resolution,
	// transcript + subagent tailing, AND the UsagePricing (block + weekly) + Limits
	// (rate_limits) folds. Poller.Snapshot reads the block + all per-session
	// projections from it, and the daemon tick reads limits/weekly from it. The old
	// inline poller path, the pricer's whole-corpus WalkDir, and the
	// SiblingLimitsSource walk were removed in pg2-66h9g — the Monitor is the only
	// corpus reader. Synchronous on the tick goroutine (no new concurrency); the
	// metric recorder is threaded via p.SetPhaseRecorder, which fans out to it and
	// the provider cache. The Discoverer reads process env through the provider
	// cache (session-id keyed, WhilePIDAlive), so `ps -E` runs once per session,
	// not once per tick.
	mon := corpus.New(claudeHome, &session.Discoverer{
		SessionsDir: session.DefaultSessionsDir(),
		PidAlive:    session.DefaultPidAlive,
		ReadEnv:     cache.Env,
	})
	mon.Register(corpus.NewSessionSnapshotObserver())
	mon.Register(corpus.NewSubagentErrorObserver())
	mon.Register(corpus.NewUsagePricingObserver(acct.PriceTable()))
	mon.Register(corpus.NewLimitsObserver())
	p.Monitor = mon

	blockTr := block.NewTracker()
	weekTr := week.NewTracker()

	return p, blockTr, weekTr
}
