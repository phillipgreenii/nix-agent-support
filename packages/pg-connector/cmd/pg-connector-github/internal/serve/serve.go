// Package serve assembles the pg-connector-github daemon from the parts the
// sibling packages built: the process shell (state directory, lock, socket,
// drain), the request router and its gate, the store, the budget governor, the
// scheduler, the summary group and the query runner, the group refresh engine
// with its six group fetchers, the viewer identity, the posted-review sidecar,
// the logs and the telemetry, and the Phase 2 and Phase 3a ops.
//
// # Startup order
//
//  1. The rendered config is already read (the binary loaded it).
//  2. The state directory is created (0700), the single-instance lock is taken,
//     a stale socket is removed and the socket is opened (proc.Start), so a
//     supervisor's health check connects at once.
//  3. The router's gate is closed: every op answers unavailable with a
//     "migrating:" message, and the socket server starts accepting.
//  4. The store is opened and migrated. A schema NEWER than this binary refuses
//     to start (a log line, exit 5, the file untouched); a corrupt store or a
//     failed migration is moved aside with a timestamp, logged, and the daemon
//     starts empty (the governor's caps are what limit the refill). While a
//     migration runs the gate's message carries its progress.
//  5. The event log, the telemetry and the governor come up; the governor's
//     pause changes and every spend go to the structured log, the event log and
//     the telemetry.
//  6. The viewer is resolved (a failure is not fatal: the daemon is then
//     unauthenticated or retrying, and says so in auth_status).
//  7. The posted-review sidecar is opened from the daemon's own environment
//     (a failure is logged and the write path answers unavailable; startup
//     goes on), the metered gh runner and Provider are built, the group
//     engine is built and the six group fetchers (detail, conversation, files,
//     commits, pending, runs) are registered with it, the revalidation handler
//     is installed on the summary service's OnSummaryRefreshed hook, the
//     telemetry observer is installed, and the three identity flushes are
//     registered (the @me query flush, the pending-group flush and the
//     sidecar-view flush).
//  8. The ops are registered: pr list, auth_status, status, show, files,
//     commits, review_pending, review_submit, search, list_activity, refresh and
//     explain; ci auth_status, status, list_runs, get_logs, rerun_failed,
//     refresh and explain. Every other op stays unknown_op.
//  9. The scheduler's queue is rebuilt from the stored due times, the summary
//     service's first and the group engine's after it (nothing is fetched by
//     the rebuild itself: the governor admits every spend, so a start after a
//     long outage catches up at the governor's rate), and the scheduler starts.
//  10. The gate opens.
//
// A SIGTERM (ctx ending) drains: the listener closes, in-flight requests finish,
// then the registered hooks run in order (the engine's in-flight writes finish,
// the scheduler and the background loops stop, the telemetry flushes, and the
// store's WAL checkpoint and close come last) and the lock is released.
//
// # What is not here
//
// The change engine (classifier, settle barrier, changes and changes_ack, the
// per-group baselines) belongs to a later phase; its ops are not registered, so
// the router answers unknown_op for them. The revalidation hook is composed
// with chain so that phase can add its own handler after the revalidation one.
package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ciprovider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/auth"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/ciops"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/diag"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/list"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/passthrough"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/prread"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/review"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/pending"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/revalidate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/sidecar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Exit codes of Run. 1 stays reserved for unexpected errors and 2 for usage.
const (
	// ExitOK is a clean drain.
	ExitOK = 0
	// ExitRefused is a serve that refused to start or failed while draining:
	// the lock is held, the socket path is too long, the state directory is
	// unusable, the store is newer than this binary, a drain hook failed.
	ExitRefused = 5
)

// Instances are the instance names the daemon serves.
var Instances = []string{"pr", "ci"}

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
	// has opened. They are seams for an in-process test (a peer with another
	// uid cannot be made by a test that is not root; the registered op table is
	// read from the router); the binary leaves them nil.
	PeerUID func(net.Conn) (uint32, error)
	OwnUID  func() uint32
	OnReady func(rt *router.Router)

	// MeterProvider is where the daemon's instruments go; nil means the global
	// provider telemetry.Setup installed. It is a seam for an in-process test
	// that reads the metrics (the revalidation counter) from a manual reader; the
	// binary leaves it nil.
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
		fmt.Fprintf(stderr, "pg-connector-github serve: %v\n", err)
		return ExitRefused
	}
	log.LockAcquired(lockPath)

	d := &daemon{o: o, cfg: cfg, log: log, ev: logs.NewEventLog(cfg.StateDir, o.Version)}
	d.rt = router.New(router.Options{
		Instances:   Instances,
		CheckConfig: func(block json.RawMessage) error { return config.CheckBlock(cfg, block) },
	})
	d.rt.SetMigrating(true, "the daemon is starting")
	d.rt.Use(d.ev.Middleware())

	// The hooks are registered now, in the order they must run, and act on
	// whatever startup got to.
	drain := &proc.Hooks{}
	drain.Register("writes", d.drainWrites)
	drain.Register("background", d.stopBackground)
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
		fmt.Fprintf(stderr, "pg-connector-github serve: %v\n", err)
		return ExitRefused
	}

	if err := <-served; err != nil {
		fmt.Fprintf(stderr, "pg-connector-github serve: %v\n", err)
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

	gov    atomic.Pointer[governor.Governor]
	sched  atomic.Pointer[scheduler.Scheduler]
	engine atomic.Pointer[groupsvc.Service]
}

// start runs startup steps 4 to 10 (see the package comment).
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
	tel, err := telemetry.New(telemetry.Options{
		MeterProvider: d.o.MeterProvider,
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
	onBatch := func(ids []string, role scheduler.Role) {
		if tel != nil {
			tel.OnBatchSent(ids, role)
		}
	}

	// The governor: its caps are what limit the refill after a move-aside and
	// the catch-up after a long outage.
	gov, err := governor.New(ratefeed.GovernorConfig(cfg.Settings), governor.Options{
		Store:         st,
		Logger:        d.log,
		OnPauseChange: d.log.OnPauseChange,
		OnSpend:       onSpend,
	})
	if err != nil {
		return fmt.Errorf("budget governor: %w", err)
	}
	d.gov.Store(gov)
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

	// The origin: gh through the token-protected gateway, with the headers, the
	// rate-limit classification and the bounded retry (origin.go).
	cli := identity.NewGH(cfg.Host, cfg.GH)
	gh := newOrigin(cli, gov, nil, github.DefaultRetryPolicy())
	feed := ratefeed.New(gov, gh, nil)

	ent := entity.NewIdentity(st, nil)
	tracker := identity.New(identity.Options{
		Host: cfg.Host, GH: gh, Log: d.log, Governor: gov, Feed: feed,
		Interval: hooks.ViewerInterval, RetryInterval: hooks.ViewerInterval,
	})

	// The summary service is built before the group engine (the engine needs
	// it), and its OnSummaryRefreshed hook is fixed at construction, so the hook
	// is a late-bound closure: the handler is installed below, once the engine
	// and the revalidation handler exist.
	var onRefreshed atomic.Pointer[func(*summary.Summary, summary.Stored)]
	svc, err := summary.NewService(summary.ServiceConfig{
		Store: st,
		Persister: &summary.Persister{
			Store: st, Identity: ent, TTL: cfg.SummaryTTL.Std(), TerminalTTL: cfg.TerminalTTL.Std(),
		},
		Fetcher:         &summary.Fetcher{GH: gh, Governor: gov, Feed: feed, BatchSize: cfg.BatchSize},
		Identity:        ent,
		Scheduler:       sched,
		KeepaliveWindow: cfg.KeepaliveWindow.Std(),
		Expiry:          cfg.Expiry.Std(),
		BatchSize:       cfg.BatchSize,
		OnBatchSent:     onBatch,
		OnSummaryRefreshed: func(prev *summary.Summary, next summary.Stored) {
			if h := onRefreshed.Load(); h != nil {
				(*h)(prev, next)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("summary service: %w", err)
	}
	runner, err := query.New(query.Config{
		Settings: cfg, Store: st, Identity: ent, Tracker: svc, GH: gh,
		Governor: gov, Feed: feed, Scheduler: sched, Auth: tracker,
	})
	if err != nil {
		return fmt.Errorf("query runner: %w", err)
	}

	// The posted-review sidecar, from the daemon's own environment. Without a
	// state home the daemon runs on: review_submit answers unavailable and
	// review_pending reports no last_append (both structs' zero values mean
	// unconfigured).
	pstore, plocker, serr := sidecar.Open(cfg, os.Getenv)
	if serr != nil {
		d.log.Log("sidecar_unavailable", "error", serr.Error())
		pstore, plocker = posted.Store{}, posted.Locker{}
	}
	view := &pending.SidecarView{Store: pstore}

	// The metered runner and the Provider over it: every origin call of the
	// groups and of the pass-through and write ops is admitted, limited and
	// booked by the governor. The runner's Feed runs over the RAW gh gateway,
	// never over the runner itself (and never over the origin, whose --include
	// handling the Feed does on its own).
	meteredFeed := ratefeed.New(gov, cli, nil)
	mr := &metered.Runner{Inner: cli, Governor: gov, Feed: meteredFeed}
	prov := metered.NewProvider(mr)

	// The group engine and the six fetchers.
	rules := groups.NewRules(cfg.Settings)
	engine, err := groupsvc.New(groupsvc.Config{
		Store: st, Scheduler: sched, Summary: svc, Rules: rules, Aliases: ent,
		Resolver: &entity.Resolver{Identity: ent, GH: gh, Governor: gov, Feed: feed, BatchSize: cfg.BatchSize},
		Identity: tracker, Governor: gov, Expiry: cfg.Expiry.Std(),
	})
	if err != nil {
		return fmt.Errorf("group engine: %w", err)
	}
	d.engine.Store(engine)
	// The detail fetcher retries and falls back per PR by itself, so its gh is
	// the origin WITHOUT the retrying runner: a batch 502 would otherwise be
	// retried first and the per-PR retries would multiply.
	runsFetcher := &runs.Fetcher{Feed: meteredFeed, Store: st, Rules: *rules, Settings: cfg.Settings}
	engine.Register(&detail.Fetcher{GH: newOriginOnly(cli, gov, nil), Governor: gov, Feed: feed, BatchSize: cfg.BatchSize})
	engine.Register(&conversation.Fetcher{Reader: prov})
	engine.Register(&prfacts.FilesFetcher{Provider: prov})
	engine.Register(&prfacts.CommitsFetcher{Provider: prov})
	engine.Register(&pending.Fetcher{Reader: prov})
	engine.Register(runsFetcher)

	// Revalidation and the telemetry observer (the same observer value serves
	// the engine's staleness events and the handler's revalidation counter).
	var observer revalidate.Observer
	if tel != nil {
		o := telemetry.NewGroupObserver(tel)
		engine.SetObserver(o)
		observer = o
	}
	reval := revalidate.New(revalidate.Config{
		Store: st, Engine: engine, KeepAlive: svc.InKeepAlive, Rules: rules, Observer: observer,
		OnError: func(err error) { d.log.Log("revalidation_failed", "error", err.Error()) },
	})
	hook := chain(reval.OnSummaryRefreshed)
	onRefreshed.Store(&hook)

	// A viewer switch flushes every stored result that depends on the viewer
	// and re-queues the configured queries at once; the pending groups and the
	// sidecar view are identity-scoped too.
	flushMe := identity.FlushMeQueries(st, func(key string) []string {
		name, _, _ := cutQueryKey(key)
		expr, _ := runner.Expr(name)
		return expr
	})
	tracker.RegisterFlush("viewer_queries", func(ctx context.Context) error {
		if err := flushMe(ctx); err != nil {
			return err
		}
		return runner.Schedule(ctx)
	})
	pending.RegisterFlushes(tracker, st, view, nil)

	// The viewer. A failure is not fatal: the tracker keeps the error, marks an
	// authentication failure, and the loop below retries.
	vctx, cancelV := context.WithTimeout(ctx, viewerStartBudget)
	if err := tracker.Refresh(vctx); err != nil {
		d.log.Log("viewer_refresh_failed", "error", err.Error())
	}
	cancelV()
	if ctx.Err() != nil {
		return errCancelled
	}
	d.background(bgCtx, "viewer", func(ctx context.Context) { d.viewerLoop(ctx, tracker) })

	// The ops.
	static := status.StaticFromConfig(cfg, d.o.Version)
	d.started = time.Now()
	auth.Register(d.rt, tracker)
	if err := status.Register(d.rt, status.Config{
		Static: static, Budgets: gov, Scheduler: sched, Queries: st, Identity: tracker, Started: d.started,
	}); err != nil {
		return err
	}
	if err := list.Register(d.rt, list.Config{
		Queries: runner, Summaries: svc, Store: st, Identity: ent, Auth: tracker, Expiry: cfg.Expiry.Std(),
	}); err != nil {
		return err
	}
	for op, h := range prread.Handlers(prread.Deps{Engine: engine, Sidecar: view}) {
		d.rt.Register("pr", op, h)
	}
	d.rt.Register("pr", review.OpName, review.Handler(review.Deps{
		Engine: engine, Provider: prov, Store: pstore, Locker: plocker,
	}))
	for op, h := range passthrough.Handlers(passthrough.Deps{GH: prov}) {
		d.rt.Register("pr", op, h)
	}
	for op, h := range ciops.Handlers(ciops.Deps{
		Engine: engine, Feed: meteredFeed, Logs: ciprovider.NewWithDeps(mr, nil), Runs: runsFetcher,
	}) {
		d.rt.Register("ci", op, h)
	}
	// refresh and the extended explain (which replaces the Phase 2 explain
	// registration: the router refuses a duplicate).
	dd := diag.Deps{
		Engine: engine, Summaries: svc, Queries: runner, Config: cfg, Store: st, Aliases: ent,
		Queue: sched, Rules: rules, Jobs: st,
	}
	for _, handlers := range []map[string]map[string]scriptout.OpHandler{diag.RefreshHandlers(dd), diag.ExplainHandlers(dd)} {
		for instance, ops := range handlers {
			for op, h := range ops {
				d.rt.Register(instance, op, h)
			}
		}
	}

	// The scheduler's queue, from the stored due times.
	if _, err := svc.Rebuild(ctx); err != nil {
		return fmt.Errorf("rebuild the scheduler queue: %w", err)
	}
	if _, err := engine.Rebuild(ctx); err != nil {
		return fmt.Errorf("rebuild the group tasks: %w", err)
	}
	if err := runner.Schedule(ctx); err != nil {
		return fmt.Errorf("schedule the configured queries: %w", err)
	}
	d.background(bgCtx, "scheduler", func(ctx context.Context) { sched.Run(ctx) })
	d.background(bgCtx, "sweep", func(ctx context.Context) { d.sweepLoop(ctx, svc, runner, st, tel) })

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

// viewerStartBudget bounds the viewer call made during startup: a gh that hangs
// must not hold the gate closed for long.
const viewerStartBudget = 20 * time.Second

// defaultSweepInterval is how often expiry runs.
const defaultSweepInterval = 10 * time.Minute

// cutQueryKey splits a stored query key "name" or "name?args" into its parts.
func cutQueryKey(key string) (name, args string, adhoc bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '?' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
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
	d.mu.Unlock()
	return nil
}

// store is the open store (nil before startup opened it).
func (d *daemon) store() *store.Store {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

// background runs fn until the daemon drains.
func (d *daemon) background(ctx context.Context, name string, fn func(ctx context.Context)) {
	d.mu.Lock()
	if d.draining { // the drain already began; nothing new starts
		d.mu.Unlock()
		return
	}
	d.bg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.bg.Done()
		defer func() {
			if r := recover(); r != nil {
				d.log.Log("background_panicked", "loop", name, "panic", fmt.Sprint(r))
			}
		}()
		fn(ctx)
	}()
}

// viewerLoop re-resolves the viewer every interval (sooner after a failure).
func (d *daemon) viewerLoop(ctx context.Context, t *identity.Tracker) {
	interval := d.o.Hooks.ViewerInterval
	if interval <= 0 {
		interval = identity.DefaultInterval
	}
	retry := d.o.Hooks.ViewerInterval
	if retry <= 0 {
		retry = identity.DefaultRetryInterval
	}
	wait := interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = interval
		if err := t.Refresh(ctx); err != nil {
			wait = retry
			d.log.Log("viewer_refresh_failed", "error", err.Error())
		}
	}
}

// sweepLoop applies expiry on a timer: the summary service's sweep, the job
// table's horizon prune and the configured queries' expiry, and reports what the
// first two evicted to the store.evictions metric (tel may be nil).
func (d *daemon) sweepLoop(ctx context.Context, svc *summary.Service, runner *query.Runner, st *store.Store, tel *telemetry.Telemetry) {
	interval := d.o.Hooks.SweepInterval
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		evicted := 0
		if n, err := svc.Sweep(ctx); err != nil {
			d.log.Log("expiry_sweep_failed", "error", err.Error())
		} else {
			evicted += n
		}
		if n, err := runs.PruneJobs(ctx, st, time.Now(), d.cfg.Expiry.Std()); err != nil {
			d.log.Log("job_prune_failed", "error", err.Error())
		} else {
			evicted += int(n)
		}
		if tel != nil {
			tel.RecordEvictions(evicted)
		}
		if _, err := runner.Expire(ctx, d.cfg.Expiry.Std()); err != nil {
			d.log.Log("query_expiry_failed", "error", err.Error())
		}
	}
}

// chain returns the function that calls each given summary hook in order. It
// mutates nothing and holds no state; the change engine's handler is chained
// after the revalidation one.
func chain(fns ...func(*summary.Summary, summary.Stored)) func(*summary.Summary, summary.Stored) {
	return func(prev *summary.Summary, next summary.Stored) {
		for _, fn := range fns {
			fn(prev, next)
		}
	}
}

// drainWrites waits for the in-flight writes of the group engine to finish
// (within the drain deadline), before the background stops and before the WAL
// checkpoint and the lock release.
func (d *daemon) drainWrites(ctx context.Context) error {
	if e := d.engine.Load(); e != nil {
		return e.DrainWrites(ctx)
	}
	return nil
}

// stopBackground ends the scheduler and the loops, after the in-flight requests
// finished (the drain runs hooks only then).
func (d *daemon) stopBackground(context.Context) error {
	d.mu.Lock()
	cancel := d.bgCancel
	d.draining = true
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.bg.Wait()
	return nil
}

// stopTelemetry flushes the exporters.
func (d *daemon) stopTelemetry(ctx context.Context) error {
	d.mu.Lock()
	shutdown := d.telShutdown
	d.mu.Unlock()
	if shutdown == nil {
		return nil
	}
	return shutdown(ctx)
}

// closeStore is the last hook: the WAL checkpoint, then the close.
func (d *daemon) closeStore(context.Context) error {
	st := d.store()
	if st == nil {
		return nil
	}
	cerr := st.Checkpoint()
	if err := st.Close(); err != nil && cerr == nil {
		cerr = err
	}
	return cerr
}
