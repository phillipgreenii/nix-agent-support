package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	jira "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/notice"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"

	opsfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/feed"
)

// Instance is the one router instance of the daemon: every op of the Jira
// backend (reads, notices, feed, status, explain) is registered under it.
const Instance = "issue"

// Exit codes of Run. 1 stays reserved for unexpected errors and 2 for usage.
const (
	// ExitOK is a clean drain.
	ExitOK = 0
	// ExitRefused is a serve that refused to start or failed while draining:
	// the lock is held, the socket path is too long, the state directory is
	// unusable, the store is newer than this binary, a drain hook failed.
	ExitRefused = 5
)

// Options configure Run.
type Options struct {
	// Config is the loaded config (REQUIRED).
	Config *config.Config
	// Version is the stamped build version, also the build id the socket frames
	// carry.
	Version string
	// Stderr receives the reason a serve refused to start; nil discards it.
	Stderr io.Writer
	// Hooks are the timing seams of a test build; the zero value is production.
	Hooks Hooks

	// PeerUID and OwnUID override the process shell's peer-credential call and
	// the daemon's own uid, and OnReady is called with the router once the gate
	// has opened. They are seams for an in-process test; the binary leaves them
	// nil.
	PeerUID func(net.Conn) (uint32, error)
	OwnUID  func() uint32
	OnReady func(rt *router.Router)

	// MeterProvider is where the daemon's instruments go; nil means the global
	// provider telemetry.Setup installed. It is a seam for an in-process test
	// that reads the metrics; the binary leaves it nil.
	MeterProvider metric.MeterProvider
}

// Run runs the daemon until ctx ends and returns the process exit code.
func Run(ctx context.Context, o Options) int {
	cfg := o.Config
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	log := logs.NewStructuredLog(cfg.StateDir, o.Version)
	lockPath := filepath.Join(cfg.StateDir, config.LockFileName)

	srv, err := proc.Start(proc.Config{
		StateDir:   cfg.StateDir,
		SocketPath: cfg.SocketPath,
		LockPath:   lockPath,
		Logger:     log,
		PeerUID:    o.PeerUID,
		OwnUID:     o.OwnUID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira serve: %v\n", err)
		return ExitRefused
	}
	log.LockAcquired(lockPath)

	d := &daemon{o: o, cfg: cfg, log: log, ev: logs.NewEventLog(cfg.StateDir, o.Version)}
	d.rt = router.New(router.Options{
		Instances:   []string{Instance},
		CheckConfig: func(block json.RawMessage) error { return config.CheckBlock(cfg, block) },
	})
	d.rt.SetMigrating(true, "the daemon is starting")
	d.rt.Use(d.ev.Middleware())

	// The hooks are registered now, in the order they must run, and act on
	// whatever startup got to.
	drain := &proc.Hooks{}
	drain.Register("writes", d.drainWrites)
	drain.Register("background", d.stopBackground)
	drain.Register("changes", d.flushChanges)
	drain.Register("telemetry", d.stopTelemetry)
	drain.Register("store", d.closeStore)

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	served := make(chan error, 1)
	go func() {
		served <- srv.Serve(serveCtx, socket.Handler(d.rt, socket.ServerConfig{Build: o.Version, Logger: log}), drain)
	}()

	if err := d.start(ctx); err != nil {
		stopServe()
		<-served
		if ctx.Err() != nil {
			// The shutdown signal arrived during startup: whatever startup was
			// doing failed because of it, and the drain above is the answer.
			return ExitOK
		}
		fmt.Fprintf(stderr, "pg-connector-issue-jira serve: %v\n", err)
		return ExitRefused
	}

	if err := <-served; err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira serve: %v\n", err)
		return ExitRefused
	}
	return ExitOK
}

// errCancelled marks a startup cut short by the shutdown signal.
var errCancelled = fmt.Errorf("startup cancelled")

// daemon is the assembled daemon's state: what the drain hooks release.
type daemon struct {
	o   Options
	cfg *config.Config
	log *logs.StructuredLog
	ev  *logs.EventLog
	rt  *router.Router

	started time.Time

	// mu guards what startup sets and a drain hook (another goroutine) reads.
	mu          sync.Mutex
	st          *store.Store
	telShutdown func(context.Context) error
	bgCancel    context.CancelFunc
	draining    bool
	bg          sync.WaitGroup

	// opened is what store.Open reported (its Outcome says whether the feed must
	// record a moved-aside store); set once by openStore, before anything else
	// reads it.
	opened store.Result

	gov    atomic.Pointer[governor.Governor]
	sched  atomic.Pointer[scheduler.Scheduler]
	detail atomic.Pointer[detail.Service]
	// chgFeed and settle are the change engine's parts the telemetry gauges and
	// the drain reach; nil until startup built them (a hook that finds nil does
	// nothing).
	chgFeed atomic.Pointer[libfeed.Feed]
	settle  atomic.Pointer[engine.Lifecycle]
}

// start runs startup steps 4 to 9 (see the package comment).
func (d *daemon) start(ctx context.Context) error {
	cfg := d.cfg
	hooks := d.o.Hooks

	bgCtx, bgCancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.bgCancel = bgCancel
	d.mu.Unlock()

	if err := d.openStore(); err != nil {
		return err
	}
	st := d.store()
	if ctx.Err() != nil {
		return errCancelled
	}

	// Logs and telemetry.
	shutdown, err := telemetry.Setup(telemetry.Config{Endpoint: cfg.OTelEndpoint, ServiceVersion: d.o.Version})
	if err != nil {
		d.log.Log("telemetry_setup_failed", "error", err.Error())
		shutdown = func(context.Context) error { return nil }
	}
	d.mu.Lock()
	d.telShutdown = shutdown
	d.mu.Unlock()
	// ONE holder keeps the origin's latest rate-limit reading for the gauges, the
	// status op and the runner that takes it.
	readings := origin.NewReadings()
	tel, err := telemetry.New(telemetry.Options{
		MeterProvider: d.o.MeterProvider,
		Readings:      readings,
		Floor:         cfg.RateReserveRequests,
		StorePath:     filepath.Join(cfg.StateDir, config.StoreFileName),
		Governor: func() governor.State {
			if g := d.gov.Load(); g != nil {
				return g.State()
			}
			return governor.State{}
		},
		Scheduler: func() scheduler.State {
			if s := d.sched.Load(); s != nil {
				return s.State()
			}
			return scheduler.State{}
		},
		KnownOp: func(instance, op string) bool { return slices.Contains(d.rt.Ops(instance), op) },
		Tracked: func(ctx context.Context) (summary.Counts, error) {
			members, err := query.Members(ctx, st)
			if err != nil {
				return summary.Counts{}, err
			}
			return summary.Classes(ctx, st, members, time.Now(), cfg.TerminalTTL.Std())
		},
		RowsWithoutKind: func(ctx context.Context) (int, error) { return telemetry.CountRowsWithoutKind(ctx, st) },

		// The change engine's polled gauges. The feed is built after the
		// telemetry (it needs the query runner), so its positions are
		// late-bound: until it exists they report nothing.
		Pending:   st.CountPending,
		Watched:   func(ctx context.Context) (int, error) { return st.CountWatched(ctx, changes.Spec().EntityKind) },
		Queries:   func(ctx context.Context) ([]store.QueryRow, error) { return st.ListQueries(ctx, true) },
		Started:   time.Now(),
		Consumers: d.consumerPositions,
		ConfiguredQueries: func() []string {
			names := make([]string, 0, len(cfg.Queries))
			for name := range cfg.Queries {
				names = append(names, name)
			}
			slices.Sort(names)
			return names
		},
	})
	if err != nil {
		d.log.Log("telemetry_init_failed", "error", err.Error())
		tel = nil
	}
	if tel != nil {
		d.rt.Use(tel.Middleware())
	}
	onSpend := func(ev governor.SpendEvent) {
		d.ev.OnSpend(ev)
		if tel != nil {
			tel.OnSpend(ev)
		}
	}

	// The governor: its cap is what limits the refill after a move-aside and the
	// catch-up after a long outage.
	gov, err := governor.New(origin.GovernorConfig(cfg), governor.Options{
		Store:         st,
		Logger:        d.log,
		OnPauseChange: d.log.OnPauseChange,
		OnSpend:       onSpend,
	})
	if err != nil {
		return fmt.Errorf("budget governor: %w", err)
	}
	d.gov.Store(gov)
	if bgCap, over := origin.WorkersOverBackgroundCap(gov, cfg.Workers); over {
		d.log.WorkersOverBackgroundCap(cfg.Workers, bgCap, cfg.RequestRatePerSecond)
	}
	d.background(bgCtx, "governor_tick", func(ctx context.Context) {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				gov.Tick()
			}
		}
	})

	sched := scheduler.New(scheduler.Options{
		Gate:         gov,
		Workers:      cfg.Workers,
		FillHorizon:  cfg.FillHorizon.Std(),
		RetryDelay:   hooks.RetryDelay,
		PollInterval: hooks.PollInterval,
	})
	d.sched.Store(sched)

	// The write-notice table and the guard store every group writer commits
	// through: a held entity stores nothing. The table's expiry works on the
	// raw store.
	table := notice.NewTable(notice.TableConfig{
		Store: st, Lease: cfg.WriteLease.Std(),
		OnError: func(err error) { d.log.Log("write_hold_expiry_failed", "error", err.Error()) },
	})
	guard := notice.NewGuardStore(st, table)

	// The change engine detects inside the store transactions of the summary
	// persister and the detail service, so it is built before them.
	chg, err := changes.New(changes.Config{
		SettleTimeout: cfg.SettleTimeout.Std(),
		BodyCap:       int(cfg.ChangeBodyCap),
	})
	if err != nil {
		return fmt.Errorf("change engine: %w", err)
	}
	chg.SetObserver(tel.ChangeObserver())

	// ONE authentication tracker is shared by the origin runner, the query runner
	// and the detail service (and its identity feeds the status report).
	auth := origin.NewAuthState()
	runOpts := origin.Options{
		Inner:        origin.NewCLIRunner(cfg),
		Governor:     gov,
		Concurrency:  cfg.Concurrency,
		DefaultPause: cfg.DefaultPause.Std(),
		Auth:         auth,
		Floor:        int64(cfg.RateReserveRequests),
		Readings:     readings,
	}
	if tel != nil {
		runOpts.Observer = tel
	}
	runner, err := origin.New(runOpts)
	if err != nil {
		return fmt.Errorf("origin runner: %w", err)
	}
	backend := jira.New(runner)

	rules := groups.Rules(groups.Settings{
		SummaryTTL: cfg.SummaryTTL.Std(), DetailTTL: cfg.DetailTTL.Std(),
		DetailMaxAge: cfg.DetailMaxAge.Std(), TerminalTTL: cfg.TerminalTTL.Std(),
	})
	keepAlive := summary.KeepAlive{Store: st, TerminalTTL: cfg.TerminalTTL.Std()}
	persister := &summary.Persister{Store: guard, Rules: rules, Hook: chg.DetectSummary, OnGone: chg.DetectGone}
	sumSvc, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: persister, Fetcher: runner, Scheduler: sched, KeepAlive: keepAlive,
		Expiry: cfg.Expiry.Std(), BatchSize: cfg.BatchSize,
		Members: func(ctx context.Context) (map[string]bool, error) { return query.Members(ctx, st) },
	})
	if err != nil {
		return fmt.Errorf("summary service: %w", err)
	}
	detSvc, err := detail.NewService(detail.Config{
		Store: guard, Scheduler: sched, Rules: rules, Aliases: summary.NewAliases(st),
		KeepAlive: keepAlive, Governor: gov, Auth: auth, Origin: runner, Chg: chg,
		Facts: backend.OperatorFacts, Expiry: cfg.Expiry.Std(), FallbackPause: cfg.DefaultPause.Std(),
		BodyCap: int(cfg.ChangeBodyCap),
		OnError: func(err error) { d.log.Log("revalidation_failed", "error", err.Error()) },
	})
	if err != nil {
		return fmt.Errorf("detail service: %w", err)
	}
	d.detail.Store(detSvc)
	if tel != nil {
		detSvc.Group().SetObserver(tel)
	}
	// Both services exist: an expiring write hold may re-pull, and a stored
	// summary revalidates the detail group.
	table.Bind(sumSvc, detSvc)
	persister.AfterCommit = detSvc.Revalidate

	qrun, err := query.NewRunner(query.RunnerConfig{
		Cfg: cfg, Store: st, Persister: persister, Fetcher: runner, Scheduler: sched,
		Chg: chg, Summary: sumSvc, Auth: auth, Log: d.log.Log,
	})
	if err != nil {
		return fmt.Errorf("query runner: %w", err)
	}

	// The change feed over the query runner's stored membership, and the settle
	// lifecycle that ends the pending rows (its queue is the detail group). A
	// failure of either aborts startup. A store that was moved aside has lost the
	// change log and every cursor, so the feed is told, and the first poll of
	// every key answers cursor_expired.
	chgFeed, err := opsfeed.New(opsfeed.Config{
		Store: st, Queries: opsfeed.NewQueries(qrun, st),
		Retention: cfg.ChangeLogRetention.Std(), ConsumerStaleAfter: cfg.ConsumerStaleAfter.Std(),
		Observer: tel.FeedObserver(),
	})
	if err != nil {
		return fmt.Errorf("change feed: %w", err)
	}
	d.chgFeed.Store(chgFeed)
	if d.opened.Outcome == store.OutcomeMovedAside {
		if err := chgFeed.NoteMovedAside(ctx, time.Now()); err != nil {
			return fmt.Errorf("record the moved-aside store for the change feed: %w", err)
		}
	}
	settle := &engine.Lifecycle{
		Engine: chg.Engine, Store: st, Queue: detSvc.Group(), Interval: time.Second, Log: d.log.Log,
	}
	d.settle.Store(settle)

	// The ops.
	d.started = time.Now()
	if err := d.registerOps(opsWiring{
		cfg: cfg, st: st, gov: gov, sched: sched, chg: chg, auth: auth, runner: runner, backend: backend,
		rules: rules, keepAlive: keepAlive, table: table, sum: sumSvc, det: detSvc, qrun: qrun,
		feed: chgFeed, settle: settle,
	}); err != nil {
		return err
	}

	// The scheduler's queue, from the stored due times. The query runner's
	// Schedule comes FIRST: a query row whose name left the config is demoted
	// before the summary rebuild decides who is a member.
	if err := qrun.Schedule(ctx); err != nil {
		return fmt.Errorf("schedule the configured queries: %w", err)
	}
	if _, err := sumSvc.Rebuild(ctx); err != nil {
		return fmt.Errorf("rebuild the scheduler queue: %w", err)
	}
	if _, err := detSvc.Group().Rebuild(ctx); err != nil {
		return fmt.Errorf("rebuild the detail tasks: %w", err)
	}
	// Pending settle rows the last run left: re-queue the groups each awaits, or
	// end it at once when its deadline passed. After the queue rebuilds (the
	// re-queued tasks must merge with the stored ones) and before the scheduler
	// starts (nothing can complete a row concurrently).
	queued, completed, err := settle.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recover the pending change rows: %w", err)
	}
	if queued+completed > 0 {
		d.log.Log("pending_changes_recovered", "queued", queued, "completed", completed)
	}
	d.background(bgCtx, "scheduler", func(ctx context.Context) { sched.Run(ctx) })
	d.background(bgCtx, "settle", func(ctx context.Context) { settle.Run(ctx) })
	d.background(bgCtx, "sweep", func(ctx context.Context) { d.sweepLoop(ctx, qrun, st, tel, chgFeed) })

	if ctx.Err() != nil {
		return errCancelled
	}
	d.rt.SetMigrating(false, "")
	d.log.DaemonStarted(cfg.StateDir, cfg.SocketPath, cfg.Hash)
	if d.o.OnReady != nil {
		d.o.OnReady(d.rt)
	}
	return nil
}

// openStore opens the store with the gate showing the migration's progress, and
// logs what Open did. A newer schema is a refusal.
func (d *daemon) openStore() error {
	path := filepath.Join(d.cfg.StateDir, config.StoreFileName)
	prog := store.NewProgress()
	opts := store.Options{Expiry: d.cfg.Expiry.Std(), Progress: prog}
	if delay := d.o.Hooks.MigrationDelay; delay > 0 {
		ladder := store.BuiltinMigrations()
		first := ladder[0].Apply
		ladder[0].Apply = func(tx *sql.Tx, report func(string)) error {
			time.Sleep(delay)
			return first(tx, report)
		}
		opts.Migrations = ladder
	}

	stop := make(chan struct{})
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		t := time.NewTicker(25 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if s := prog.Snapshot(); s.Migrating {
					d.rt.SetMigrating(true, s.Text)
				}
			}
		}
	}()

	began := time.Now()
	st, res, err := store.Open(path, opts)
	close(stop)
	<-watching
	d.log.StoreOpened(path, res, err, time.Since(began))
	if err != nil {
		return fmt.Errorf("open the store: %w", err)
	}
	d.mu.Lock()
	d.st = st
	d.opened = res
	d.mu.Unlock()
	return nil
}

// store is the open store (nil before startup opened it).
func (d *daemon) store() *store.Store {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

// consumerPositions is the telemetry's source of consumer positions: the feed's,
// or none until the feed exists.
func (d *daemon) consumerPositions(ctx context.Context) ([]libfeed.Position, error) {
	f := d.chgFeed.Load()
	if f == nil {
		return nil, nil
	}
	return f.Positions(ctx)
}
