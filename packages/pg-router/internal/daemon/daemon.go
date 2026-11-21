package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/calstate"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/settle"

	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// HandlerCommandFor builds the wireclient.CommandFor seam BootCore and
// runRunRole both hand to wireclient.New — the "deployment/wiring layer"
// internal/wireclient's own package doc names as the caller who answers
// "which command does this role's registered handler participant run".
//
// A role is invoked only through its own handler (docket pg2-12gr4.10, the
// handler split): the argv is [role.Handler, subcommand], and the role's
// [role.config] rides on stdin as the request's role object
// (internal/wireclient). ONE resolver (config.Config.RoleCommand) serves this
// seam, config --show, config check, run-role, doctor and startup validate. The
// subcommand is argv[1], right after the handler, because the handler's own CLI
// parses its first argument as the subcommand.
func HandlerCommandFor(cfg config.Config) wireclient.CommandFor {
	return cfg.RoleCommand
}

// BootCore loads the durable queue, registers a queue->executor Listener
// (orchestrator.NewListener) for every ENABLED role, and starts the core socket
// service (internal/core.Listen) — the run/run-until-idle wiring bead pg2-f3mcb.2
// adds. Before this, nothing outside internal/core's own tests called
// Listen+Accept; production `drain` ran the retired internal/eventbus +
// internal/orchestrator.DrainOnce path instead.
//
// It also wires ONE metrics.Emitter into every production seam that can drive
// it — eventqueue.WithObserver at queue construction, core.Options.Observer
// at Listen, o.SourceFailureObserver for ProduceTick's discover.Produce
// call, and o.HandlerFailureObserver for roleListener.Offer's own non-panic
// handler-error return — so a single emitter answers eventqueue.Observer,
// core.IngestObserver, discover.SourceFailureObserver, and
// orchestrator.HandlerFailureObserver alike (INV-FAIL-3, register gap R21 /
// bead pg2-00jpn: before this assignment, source failures were recorded to
// logs only in the running binary — discover.WithSourceFailureObserver's seam
// existed but no production call site ever passed it a live observer; bead
// pg2-97539 closed the identical class of gap for handler-internal errors).
// The emitter is built BEFORE the queue (the queue takes it as its observer, so
// it must exist first), yet the gauge callbacks metrics.New registers must read
// the queue: they reach it through queueRef, which this function publishes once,
// as its last step, and metrics.WithQueueReady keeps every one of them silent
// until then (see queueRef; internal/metrics/metrics_test.go's newHarness, which
// only ever collects after its queue exists, closes over a plain variable
// instead). mp is resolved by ResolveMeterProvider, which defers to cfg.MeterProvider
// when the deployment has bound one (INV-OBS-1: core stays unaware of any
// concrete monitoring backend; binding a real one is a deployment concern
// this function does not take on — it is chosen by CONFIG, not hardcoded
// here, per Task 3.3's binding decision) and otherwise builds a real
// SDK-backed provider with a ManualReader — see ResolveMeterProvider's own
// doc for why that, not the plain OTel no-op, is now this function's
// default: `mon.read` (Task 3.6-prereq / Task 3.6) needs SOME live value to
// read back, which the wiring also exposes through core.Options.MetricsReader.
//

// The returned storeClose MUST be deferred by the caller: eventqueue.Queue owns
// no Close of its own (Store is an injected seam), so the file handle beneath it
// is this function's caller's to release.
//
// cfg.SerializeTypes (INV-CONC-1, `packages/pg-router/docs/decisions · DEC-CONC-1`)
// threads through as eventqueue.WithSerializeTypes the same way cfg.RetryBackoff
// threads through as WithRetryBackoff — this is the ONE production seam that
// resolves the config-level mark into the queue's dispatch-time occupancy gate;
// an empty/absent [pool].serialize_types leaves every type unaffected.
// cfg.EarlyEvictionTypes (pg2-u0p2d, DEC-EVENT-2) threads through the same way as
// eventqueue.WithEarlyEvictionTypes; an empty/absent [pool].early_eviction_types
// leaves every type on full retention.
// declaredRoles is the FULL, pre-selector role list (prepareRun captures it
// before applySelectors runs — Task 4.1, Binding Decision 4) and excluded
// is that same call's RunExclusions; both flow straight onto core.Options
// so composeStatusReply's listeners[]/sources[] can render the full
// configured participant set with `excluded` computed independently of
// `enabled` (see core.Options' own docs on DeclaredRoles/ExcludedRoles/
// ExcludedSources).
//
// runMode is core.RunModeLongRunning or core.RunModeDrainAndExit — the same
// two-value identity RunOneTick/ResolvedConfigFor already thread through
// core.TickSnapshot. BootCore uses it for exactly one decision: whether to
// pass metrics.WithLiveness when constructing the Emitter below (pg2-tp13g).
// Per the operator's DECIDED process-health semantics (pg2-tp13g: "report 1
// as long as the pg-router daemon process is up and its metrics endpoint
// responds -- do not gate it on recent dispatch activity or a heartbeat
// window"), the isLive callback itself is a constant `true`: OTel only ever
// invokes an ObservableGauge's callback while collecting a live scrape of a
// running process, so the callback firing at all already proves both facts
// this metric is defined to report — there is no further condition to
// check. runMode == core.RunModeDrainAndExit
// (runRunUntilIdle) MUST NOT pass WithLiveness at all, per Task 3.3's binding
// decision that drain-and-exit never registers the observable, not merely
// never observes it true.
func BootCore(ctx context.Context, cfg config.Config, o *orchestrator.Orchestrator, declaredRoles roles.RoleSet, excluded RunExclusions, runMode string) (svc *core.Service, q *eventqueue.Queue, mp metric.MeterProvider, storeClose func() error, err error) {
	// o.Handler (this bead, pg2-g068j): wire a real wireclient.Client so
	// dispatch/postStartup/preShutdown reach a registered handler
	// participant instead of falling through to unconfiguredHandler — the
	// "no Handler configured" gap docket pg2-oju6w's Task 5.4 left open (see
	// HandlerCommandFor's own doc). Only when unset: a caller (a test's
	// fakeHandlerClient) that pre-set o.Handler before calling BootCore MUST
	// keep its own value, exactly the same "caller wins" pattern o.Cmd/
	// o.Log already follow elsewhere in this package.
	if o.Handler == nil {
		o.Handler = wireclient.New(HandlerCommandFor(cfg))
	}
	store, err := eventqueue.NewFileStore(filepath.Join(cfg.LogDir, "queue.jsonl"))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open event queue: %w", err)
	}
	mp, metricsReader := ResolveMeterProvider(cfg)
	var metricsOpts []metrics.Option
	if runMode == core.RunModeLongRunning {
		metricsOpts = append(metricsOpts, metrics.WithLiveness(func() bool { return true }), metrics.WithBuildInfo(Version))
	}
	// queueRef is how the queue-bound gauges below reach the queue (bead pg2-nzkjl,
	// DEC-EVENT-5's residual). The queue is built after the emitter, and a scrape
	// can land in between: runRun starts the /metrics endpoint before it calls
	// BootCore, and the queue's startup compaction alone held the gap open for
	// 6.03 s on 2026-10-12. The callbacks used to close over this function's
	// still-nil q there and dereference it; the exporter's collector recovers
	// that panic into a failed scrape (HTTP 500), it does not crash the daemon.
	// queueRef is stored once, as this function's last step before it returns, by
	// which point the queue is built and every enabled role is registered, and it
	// is never cleared. metrics.WithQueueReady keeps every gauge that reads the
	// queue silent until then, so a collect before it reports no series for them
	// (neither a panic nor a zero: there is no queue to read a zero from), and a
	// collect after it never sees a half-wired queue. An atomic rather than the
	// variable the closures used to capture, because the collecting goroutine is a
	// scrape's, not this one.
	var queueRef atomic.Pointer[eventqueue.Queue]
	metricsOpts = append(metricsOpts, metrics.WithQueueReady(func() bool { return queueRef.Load() != nil }))
	// Event-queue write-ahead-log size gauge (bead pg2-8e0m6), read live off the
	// queue on each collect.
	metricsOpts = append(metricsOpts, metrics.WithQueueLogSize(func() int64 { return queueRef.Load().LogSize() }))
	// Log-size limit gauges (bead pg2-5d3ui): limit, percent, emitters halted, and
	// which reason (if any) the log is rejecting events for.
	metricsOpts = append(metricsOpts, metrics.WithLogLimitStatus(func() eventqueue.LimitStatus { return queueRef.Load().LimitStatus() }))
	// Oldest-pending-age (per type) and per-listener in-flight gauges (bead
	// pg2-n7da9), read live off the queue on each collect.
	metricsOpts = append(metricsOpts, metrics.WithQueueAges(func() map[string]time.Duration { return queueRef.Load().OldestPendingAgeByType() }))
	metricsOpts = append(metricsOpts, metrics.WithListenerInFlight(func() map[string]int { return queueRef.Load().InFlightByListener() }))
	// Per-listener pending count and oldest pending age (DEC-OBS-12), one queue pass.
	metricsOpts = append(metricsOpts, metrics.WithListenerPending(func() map[string]eventqueue.ListenerPending { return queueRef.Load().PendingByListener() }))
	// Per-role progress gauges (docket pg2-12gr4.8): last-progress age and the
	// unsuccessful and busy counts since it, one series per ENABLED role (the
	// roles that get a listener below), all starting at daemon start. The age
	// reads 0 while the role's offer is in flight (the in-flight read above).
	enabledRoles := make([]string, 0, len(cfg.Roles))
	for _, r := range cfg.Roles {
		if r.Enabled {
			enabledRoles = append(enabledRoles, r.Name)
		}
	}
	metricsOpts = append(metricsOpts, metrics.WithRoleProgress(enabledRoles...))
	metricsOpts = append(metricsOpts, metrics.WithBindings(BindingInfo(cfg.Roles)))
	// The level gauges read the projection built further down (levelProjRef is
	// stored as soon as it exists; a collect before then reports no level
	// series), and role_paused starts at 0 for every enabled role.
	var levelProjRef atomic.Pointer[core.LevelProjection]
	levelRegistry := cfg.Resources.Registry()
	metricsOpts = append(metricsOpts, metrics.WithPausedRoles(enabledRoles...), metrics.WithLevels(func() []metrics.LevelSample {
		p := levelProjRef.Load()
		if p == nil {
			return nil
		}
		return levelSamples(p, levelRegistry, time.Now())
	}))
	// Per-role availability gauge (docket pg2-12gr4.8, ruling 31): 1 while the
	// role is available, 0 while a startup validate marked it unavailable. It
	// reads o.Registry live, which BootCore sets below, before queueRef is stored
	// (the gauge stays silent until then, like every queue-bound gauge here).
	metricsOpts = append(metricsOpts, metrics.WithRoleAvailability(func() map[string]bool {
		out := make(map[string]bool, len(enabledRoles))
		for _, name := range enabledRoles {
			out[name] = o.Registry != nil && o.Registry.Available(name)
		}
		return out
	}))
	// Per-source last-success / expected-interval gauges (bead pg2-tv11a,
	// DEC-OBS-4): one series per enabled, non-excluded PULL source with a
	// period, last-success initialised to now (process start).
	metricsOpts = append(metricsOpts, metrics.WithPullSources(PullSourceIntervals(cfg)))
	if runMode == core.RunModeLongRunning {
		// Persist each source's last success under the state dir so a restart
		// continues the age instead of re-seeding it (bead pg2-yagdb2).
		metricsOpts = append(metricsOpts, metrics.WithSourceStateFile(filepath.Join(cfg.LogDir, metrics.SourceStateFileName)))
		// Calendar slot state lives beside it, so each calendar slot yields at
		// most one tick across restarts. The sleep sampler runs in its own
		// goroutine, independent of the producer loop, for the missed-slot rule.
		// run-until-idle sets neither: with no state dir it behaves as a first
		// start and nothing calendar-triggered fires.
		o.CalendarStatePath = filepath.Join(cfg.LogDir, calstate.FileName)
		o.ProcessStart = time.Now()
		o.Sampler = calstate.NewSampler(cfg.PollInterval, calstate.RealSource())
		go o.Sampler.Run(ctx)
	}
	// pg_router_busy{kind,name} (bead pg2-8ij5o): a source is busy while a fetch of
	// it is in flight, read from the same activityObs the status reply's sources[]
	// read, so the metric and status agree. activityObs is constructed here, ahead
	// of the emitter, for that reason; the ring it writes to is built just below.
	ring := activity.New(cfg.ActivityRingSize)
	activityObs := NewActivityObserver(ring)
	metricsOpts = append(metricsOpts, metrics.WithBusySources(activityObs.InFlightSources))
	emitter, err := metrics.New(ctx, mp, func() map[string]int { return queueRef.Load().DepthByType() }, metricsOpts...)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("construct metrics emitter: %w", err)
	}
	// Wire the same emitter into ProduceTick's pull-source failure retry hook
	// (INV-FAIL-3, register gap R21 / bead pg2-00jpn) — the remaining seam
	// discover.WithSourceFailureObserver's doc left for a follow-on to close.
	o.SourceFailureObserver = emitter
	// ring (built above, ahead of the emitter) is the dispatch-outcome activity buffer (Task 3.4): a SECOND
	// eventqueue.Observer, fanned out alongside emitter at this one
	// construction site rather than folded into a new composite-observer
	// abstraction. Task 3.5 is what embeds *ring in the daemon's assembled
	// state for Task 3.8's status verb to read live and directly; this
	// function's own job stops at constructing it and keeping it fed.
	// activityObs is the SAME instance wired both as one of the queue's
	// eventqueue.Observer fan-out arms below AND as o.ResourceLimitObserver
	// (this bead, pg2-fm2gw) — it must be the identical instance so
	// roleListener.Offer's inline OnResourceLimit call and the queue's own
	// later OnAccept call land in the SAME correlation map (see
	// activityObserver's own doc). o.ResourceLimitObserver is assigned here,
	// well before the role-registration loop below calls o.NewListener,
	// which captures it onto each roleListener at construction (the same
	// capture-at-construction pattern o.Registry already uses).
	o.ResourceLimitObserver = activityObs
	// activityObs ALSO implements discover.SourceActivityObserver and
	// core.SourceInFlightReader (DEC-OBS-2, bead pg2-ugcrb) — the SAME
	// instance already wired above/below as the ring's own writer picks up
	// each pull source's fetch-window/outcome signal too, exactly like the
	// ResourceLimitObserver wiring just above reuses one instance across two
	// roles rather than constructing a second object.
	o.SourceActivityObserver = activityObs
	// listenerCounts is Task 4.1 Step 5's per-role delivered/declined tally
	// (core.ListenerCounts): pre-populated for every DECLARED role (not
	// only the enabled ones) so a lookup in statusListeners always hits,
	// and bumped by listenerCountObserver at the SAME (event, handler)
	// acceptance/decline sites eventqueue.Dispatch already reports via
	// OnAccept/OnDeclined (INV-EVT-1) — never a second, independently-
	// tracked count.
	listenerCounts := make(map[string]*core.ListenerCounts, len(declaredRoles))
	for _, r := range declaredRoles {
		listenerCounts[r.Name] = &core.ListenerCounts{}
	}
	// Wire the same emitter into roleListener.Offer's own non-panic
	// handler-error hook (bead pg2-97539) — the gap where a handler-internal
	// error surfaced through wireclient's synchronous Offer call fell through
	// both existing failure classes uncounted (see
	// orchestrator.HandlerFailureObserver's own doc for the full story) — now
	// fanned out (this task) to ALSO bump a per-role handlerFailureCountObserver,
	// the same listenerCounts map listenerCountObserver already tallies
	// delivered/declined into, so composeStatusReply's listeners[] can render
	// a per-role FAIL count alongside DLVD/DECL.
	o.HandlerFailureObserver = fanOutHandlerFailureObserver{emitter, &handlerFailureCountObserver{counts: listenerCounts}}
	// pg_router_dispatch_retries_total{role,class} (bead pg2-yu5y2).
	o.DispatchRetryObserver = emitter
	// pg_router_dispatch_redrives_total{role,class} (ADR 0121).
	o.DispatchRedriveObserver = emitter
	// pg_router_dispatch_failed_total{type,role}: a dispatch that ended failed.
	o.DispatchFailedObserver = emitter
	// pg_router_dispatch_at_level_{,seconds_}total{role,resource,level[,outcome]}.
	o.DispatchLevelObserver = emitter
	// pg_router_role_{last_progress_age_seconds,unsuccessful_since_progress,
	// busy_since_progress}{role}, counted in roleListener.Offer (docket pg2-12gr4.8).
	o.RoleProgressObserver = emitter
	// pg_router_skipped_total{type,role,reason}: an offer the handler accepted and
	// completed as skipped (bead pg2-6t91v7), kept out of pg_router_throughput_total.
	o.SkippedObserver = emitter
	// pg_router_events_reoffered_after_shutdown_total{role} and
	// pg_router_supervisors_per_session{role,session}.
	o.ReofferObserver = emitter
	o.SupervisionObserver = emitter
	// pg_router_producer_load_backoff / pg_router_producer_ticks_skipped_total.
	o.LoadBackoffObserver = emitter
	// The level projection feeds the queue's admission (WithLevelSource); its
	// appends reach the queue's durable level path once the queue exists.
	levelAppender := &lateLevelAppender{}
	levelProj := core.NewLevelProjection(core.LevelProjectionConfig{
		Registry:  cfg.Resources.Registry(),
		Resources: markNoSensor(levelResources(cfg.Resources), cfg),
		Skew:      cfg.Levels.ObservedAtSkew,
		Appender:  levelAppender,
		// Every level transition is counted and logged by the metrics Emitter.
		OnReject:          emitter.OnLevelEventRejected,
		OnLevelChange:     emitter.OnLevelChanged,
		OnGraceExpired:    emitter.OnLevelGraceExpired,
		OnOverrideSet:     emitter.OnLevelOverrideSet,
		OnOverrideExpired: emitter.OnLevelOverrideExpired,
	})
	levelProjRef.Store(levelProj)
	// The pull observer counts pulls and failures per sensor.
	o.LevelPullObserver = emitter
	// Settlement report of cursor-feed sources: only built when some query
	// declares settle, so a deployment without one writes no ledger file.
	var settler *settle.Settler
	for _, src := range cfg.Queries {
		if _, _, ok := query.SettleOf(src.Query); ok {
			ledger, lerr := settle.OpenLedger(filepath.Join(cfg.LogDir, "ack-ledger.json"))
			if lerr != nil {
				_ = store.Close()
				return nil, nil, nil, nil, fmt.Errorf("open settlement ledger: %w", lerr)
			}
			settler = settle.NewSettler(ctx, ledger, func(rctx context.Context, qname, lane, token string) error {
				for _, s := range cfg.Queries {
					if s.Name == qname {
						return query.RunSettle(rctx, query.Env{RepoRoot: o.Cfg.RepoRoot, Cmd: o.Cmd}, s.Query, lane, token)
					}
				}
				return fmt.Errorf("settle: unknown query %q", qname)
			}, emitter.OnSettleFailure)
			o.Settler = settler
			break
		}
	}
	var ackHook eventqueue.Option = func(*eventqueue.Queue) {}
	if settler != nil {
		ackHook = eventqueue.WithAcceptHook(settler.OnEventSettled)
	}
	q, err = eventqueue.New(store, ackHook, eventqueue.WithRetryBackoff(cfg.RetryBackoff), eventqueue.WithObserver(fanOutObserver{emitter, fanOutObserver{activityObs, newListenerCountObserver(listenerCounts)}}), eventqueue.WithSerializeTypes(cfg.SerializeTypes...), eventqueue.WithEarlyEvictionTypes(cfg.EarlyEvictionTypes...), eventqueue.WithLevelSource(levelProj.Snapshot), eventqueue.WithRegistry(cfg.Resources.Registry()), eventqueue.WithStartStagger(cfg.Levels.StartStagger), eventqueue.WithLevelObserver(emitter),
		// Compact queue.jsonl down to live state: once at startup (before the queue
		// replays it or accepts an event) and, at runtime, whenever it outgrows
		// cfg.CompactThresholdBytes (bead pg2-8e0m6).
		eventqueue.WithCompaction(cfg.CompactThresholdBytes, true),
		// Enforce the log-size limit (bead pg2-5d3ui): the soft threshold (derived,
		// 90% of max_log_bytes) halts polled emitters, the hard limit
		// (max_log_bytes) rejects new events with a classified reason. Startup
		// compaction (above) runs before either is evaluated.
		eventqueue.WithLogLimits(cfg.SoftLogBytes(), cfg.MaxLogBytes))
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("construct event queue: %w", err)
	}
	// The level records the startup replay found go to the projection, and the
	// projection's own appends reach the durable log through the queue.
	levelAppender.q.Store(q)
	core.ReplayLevels(q, levelProj)
	// Level-source queries divert their answers to the projection's ingest.
	o.LevelIngest = levelProj
	// Bindings is built ONCE and threaded into both consumers so they can never
	// disagree about which types are declared (Task 1.1, INV-DISP-3): the same
	// value goes to core.Listen (validates PUSHED events) and onto the
	// Orchestrator (ProduceTick threads it into discover.Produce, which now
	// rejects an undeclared-type PULLED event the same way). declaredBindTypes
	// itself moved to roles.RoleSet.DeclaredBindTypes — a shared home usable by
	// anything holding a RoleSet, not just this binary's BootCore.
	bindings := core.NewBindings(cfg.Roles.DeclaredBindTypes()...)
	o.Bindings = bindings
	// sourceIntervalsMs (this task, pg2-mnf7t.1) resolves each configured
	// source's own expected tick cadence ONCE, from the full configured
	// query set, so the Sources pane can judge staleness per-source instead
	// of against the pool-wide tick interval.
	sourceIntervalsMs := make(map[string]int64, len(cfg.Queries))
	// sourceDescriptions (pg2-ec754) resolves each configured source's own
	// operator-authored free-text description ONCE, from the full
	// configured query set — mirroring sourceIntervalsMs's own resolve-
	// once-at-boot pattern immediately above.
	sourceDescriptions := make(map[string]string, len(cfg.Queries))
	for _, src := range cfg.Queries {
		sourceIntervalsMs[src.Name] = config.ExpectedIntervalMsFor(src, cfg.ExpectedIntervalOverrides)
		sourceDescriptions[src.Name] = src.Description
	}
	opts := core.Options{
		LogDir:         cfg.LogDir,
		Queue:          q,
		Bindings:       bindings,
		Observer:       emitter,
		MonitorSubsets: monitorSubsetResolverFrom(cfg.MonitorSubsets),
		// ActivityRing/ConfigPath (Task 3.8): the same ring constructed above
		// (already fed by fanOutObserver) handed to the `status` verb to read
		// live, and the resolved config path the verb echoes back informationally
		// under its `core.configPath` field.
		ActivityRing: ring,
		// Levels / LevelRefresher: the level verbs and the status levels read the
		// projection; level refresh pulls through the orchestrator's level puller.
		Levels:         levelProj,
		LevelRefresher: &levelRefresher{resources: cfg.Resources, puller: o.LevelPuller, proj: levelProj},
		// SourceInFlight (DEC-OBS-2, bead pg2-ugcrb): activityObs again, as
		// core.SourceInFlightReader — see the doc at its o.SourceActivityObserver
		// assignment above for why one instance covers both roles.
		SourceInFlight: activityObs,
		ConfigPath:     cfg.ConfigPath,
		// DeclaredRoles/ExcludedRoles/ExcludedSources/ListenerCounts
		// (Task 4.1): see this function's own doc comment above.
		DeclaredRoles:      declaredRoles,
		ExcludedRoles:      excluded.Roles,
		ExcludedSources:    excluded.Sources,
		SourceIntervalsMs:  sourceIntervalsMs,
		SourceDescriptions: sourceDescriptions,
		ListenerCounts:     listenerCounts,
		// RoleProgress: the state behind the three role-progress gauges, read for
		// the status reply's per-listener `progress` (docket pg2-12gr4.8).
		RoleProgress: emitter.RoleProgress,
	}
	if metricsReader != nil {
		// Assigned only when non-nil: metricsReader is a typed *metrics.Reader,
		// and boxing a nil one into the core.MetricsReader interface field
		// unconditionally would make Options.MetricsReader != nil hold true even
		// then (a non-nil interface can wrap a nil pointer) — this guard is what
		// keeps Service.MetricsReader() genuinely nil when ResolveMeterProvider
		// returned none (Config.MeterProvider set to an external provider).
		opts.MetricsReader = metricsReader
	}
	svc, err = core.Listen(ctx, opts)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("start core: %w", err)
	}
	// o.Registry (Task 2.3, pg2-84o3m.22) is set BEFORE the loop below so every
	// o.NewListener(ctx, r) call captures the SAME live *core.Registry onto its
	// roleListener — Offer reads it live at each future Offer call, so it does
	// not matter that this role's own RegisterInProcess/SetLifecycle promotion
	// (a few lines down, same iteration) hasn't happened yet at the moment
	// NewListener itself runs.
	o.Registry = svc.Registry()
	// The retry count lives in the queue log so a restart does not reset it (ADR 0121).
	o.RetryLog = q
	o.FailedLog = q
	// A transiently failed event of a role with max_redrives > 0 is re-enqueued here.
	o.RedriveQueue = q
	// Registration happens AFTER Listen (svc must exist) but is otherwise
	// independent of Accept: an in-process participant never dials the
	// socket, so there is no handshake to wait on. Task 2.1: register every
	// ENABLED role's listener into the registry with an in-process callback
	// marker (never Service.Register's socket-baked string —
	// core.Registry.RegisterInProcess), then promote it straight to `started`
	// (Register hard-codes `starting`; without this promotion Task 2.3's
	// availability check blocks ALL dispatch to every in-process handler).
	for _, r := range cfg.Roles {
		if !r.Enabled {
			// Declared but inactive this run: its bindings still count for
			// Bindings.Declares below (INV-DISP-3's configuration-wide view), but
			// no Listener is registered, so its events wait, are offered to
			// nobody, and expire unconsumed (INV-EVT-1, INV-EVT-4).
			slog.Info("role disabled; not registering a listener", "role", r.Name)
			continue
		}
		// A role whose handler supervises its own work (a ccpool-backed session)
		// declares it survives a shutdown (bead pg2-dtigc, ADR 0085): in daemon
		// mode its dispatch runs on a context the shutdown's cancelDispatch cannot
		// reach, so exec.CommandContext never SIGKILLs its handler. Drain-and-exit
		// keeps the cancellable ctx: an interactive interrupt still stops handlers.
		listenerCtx := ctx
		if runMode == core.RunModeLongRunning && RoleSurvives(r) {
			listenerCtx = context.WithoutCancel(ctx)
			slog.Info("role dispatch survives shutdown; it is never cancelled by this daemon", "role", r.Name)
		}
		q.Register(o.NewListener(listenerCtx, r))
		if _, err := svc.Registry().RegisterInProcess(r.Name, core.KindHandler); err != nil {
			_ = store.Close()
			return nil, nil, nil, nil, fmt.Errorf("register role %s: %w", r.Name, err)
		}
		if err := svc.Registry().SetLifecycle(r.Name, conformance.Started); err != nil {
			_ = store.Close()
			return nil, nil, nil, nil, fmt.Errorf("promote role %s to started: %w", r.Name, err)
		}
	}
	// Every role's compiled requirement, and each polled query's consumers, go to
	// the queue's level admission.
	o.RegisterLevels(q)
	// Rebuild each cursor-feed lane's settlement from the durable queue and
	// ledger and re-send the report; every listener is registered by now.
	if settler != nil {
		settler.Restart(q.AckSettled)
	}
	// The queue is built and every enabled role is registered: from the next
	// collect the queue-bound gauges report (see queueRef above).
	queueRef.Store(q)
	// pg_router_requirement_blocked_seconds_total: the long-running daemon books
	// the time each blocked role and emitter is held back, every
	// metrics.RequirementAccrualInterval. run-until-idle is a short drain and
	// does not.
	if runMode == core.RunModeLongRunning {
		go emitter.RunRequirementAccrual(ctx, metrics.RequirementAccrualInterval, q.BlockedRequirements)
	}
	return svc, q, mp, store.Close, nil
}

// PullSourceIntervals returns, per configured pull source, the expected
// interval the persistent-failure alert scales its threshold by (bead
// pg2-tv11a, DEC-OBS-4). cfg is the POST-selector config, so a selector-excluded
// source is already absent and is never exported; every configured query is a
// pull source (there is no config-level enable flag for one, and no push query
// type). A source with no period — a manual or threshold trigger with no
// explicit expected_interval — has no cadence to alert against and is omitted.
// A period trigger whose own Every is zero fires on cfg.PollInterval (the same
// fallback discover's cadence uses), so that is its interval. A calendar
// trigger's interval is the largest gap between its consecutive slots over the
// next 14 days (config.ExpectedIntervalMsFor), never the poll interval: that
// would fire the alert 30 minutes after start for a daily source.
func PullSourceIntervals(cfg config.Config) map[string]time.Duration {
	out := make(map[string]time.Duration, len(cfg.Queries))
	for _, src := range cfg.Queries {
		ms := config.ExpectedIntervalMsFor(src, cfg.ExpectedIntervalOverrides)
		if ms <= 0 {
			t := src.Query.Trigger()
			if _, threshold := query.Threshold(t); threshold || query.IsManual(t) {
				continue
			}
			ms = cfg.PollInterval.Milliseconds()
		}
		if ms > 0 {
			out[src.Name] = time.Duration(ms) * time.Millisecond
		}
	}
	return out
}

// ResolveMeterProvider decides the MeterProvider metrics.New registers the
// catalog's instruments on, and — when possible — the metrics.Reader handle
// that can read their current values back for INTF-MON's pull half
// (`mon.read`, Task 3.6-prereq).
//
// Config.Meter()'s own documented default (INV-OBS-1 / Task 3.3 binding
// decision) is the OTel no-op provider: "core stays unaware of any concrete
// monitoring backend". A no-op provider's instruments never record
// anything, so it can never answer a read-back query — read-back needs a
// REAL SDK-backed provider under the hood. Rather than relitigate Task
// 3.3's binding decision, this function keeps its semantics (no
// deployment-bound backend still selects a package default, chosen by
// config rather than hardcoded here) while making that default a real
// SDK-backed MeterProvider with a ManualReader instead of a plain no-op —
// nothing regresses, since the no-op could never be read back either way.
//
// When cfg.MeterProvider IS set (a deployment-bound external backend, e.g.
// an OTLP exporter — unused by any production caller today, verified
// against the live worktree), that provider owns its own reader set fixed
// at its own construction; the OTel SDK has no way to retrofit a second
// reader onto it here, so metricsReader is nil in that case — a documented,
// structural degradation, not an oversight.
func ResolveMeterProvider(cfg config.Config) (mp metric.MeterProvider, metricsReader *metrics.Reader) {
	if cfg.MeterProvider != nil {
		return cfg.MeterProvider, nil
	}
	return metrics.NewReadableProvider()
}

// monitorSubsetResolverFrom adapts cfg.MonitorSubsets (a plain map, Task
// 3.6-prereq) into the core.MonitorSubsetResolver function core.Options
// wants — the same config->core.Options adaptation BootCore already
// performs for Bindings above (declaredBindTypes(cfg.Roles) ->
// core.NewBindings(...)). nil input yields a nil resolver so
// Service.monitorSubsetResolver's own nil-default applies unchanged.
//
// Deliberately NO accompanying registration loop (bead pg2-bncv6): unlike
// the per-role loop below, which pre-registers each ENABLED role in-process
// because a role's Listener genuinely lives inside this same binary, a
// kind=monitor sink is a real OUT-OF-PROCESS participant with nothing here
// to pre-register on its behalf. Per docs/behavior/interfaces.md's
// `INTF-MON` section, the subset "declaration is configuration, resolved
// before the sink ever calls... register" — the sink itself calls the
// common `register` wire verb (internal/core/register.go, Task 2.1,
// pg2-84o3m.20) when it connects, and Service.Register resolves its Subset
// from THIS SAME map at that moment (register.go's handleRegister is
// already Service.Register's real production caller, for every kind
// including monitor). Pre-registering an id here instead (e.g. via
// RegisterInProcess, the role loop's own mechanism) would misuse the
// InProcessCallback marker on a genuinely external participant and
// fabricate a `started` lifecycle before any real connection exists —
// solving a problem the design does not pose.
func monitorSubsetResolverFrom(subsets map[string][]string) core.MonitorSubsetResolver {
	if len(subsets) == 0 {
		return nil
	}
	return func(id string) []string { return subsets[id] }
}

// fanOutObserver calls two eventqueue.Observers for every hook, in order
// (a first, then b). It exists ONLY to compose emitter and the activity
// ring at BootCore's one construction site (eventqueue.WithObserver keeps
// only the LAST value it's given, so two separate WithObserver calls would
// silently drop the first) — a plain, unexported, task-scoped fan-out, not a
// new reusable composite-observer abstraction (Task 3.4 Files).
type fanOutObserver struct {
	a, b eventqueue.Observer
}

func (f fanOutObserver) OnEnqueue(evt eventqueue.Event) {
	f.a.OnEnqueue(evt)
	f.b.OnEnqueue(evt)
}

// OnRestore (bead pg2-0efop) forwards eventqueue.RestoreObserver's hook to
// whichever arms implement it, so replay's restored events seed the metrics
// emitter's and the activity observer's OnAccept correlation without a fresh
// OnEnqueue (which would re-emit activity rows / double-report the enqueue).
func (f fanOutObserver) OnRestore(evt eventqueue.Event) {
	if ro, ok := f.a.(eventqueue.RestoreObserver); ok {
		ro.OnRestore(evt)
	}
	if ro, ok := f.b.(eventqueue.RestoreObserver); ok {
		ro.OnRestore(evt)
	}
}

// OnEnqueueRejected (bead pg2-5d3ui) forwards eventqueue.RejectObserver's hook to
// whichever arms implement it — the metrics emitter counts it per type and
// reason; the activity observer has no use for it.
func (f fanOutObserver) OnEnqueueRejected(evtType, reason string) {
	if ro, ok := f.a.(eventqueue.RejectObserver); ok {
		ro.OnEnqueueRejected(evtType, reason)
	}
	if ro, ok := f.b.(eventqueue.RejectObserver); ok {
		ro.OnEnqueueRejected(evtType, reason)
	}
}

func (f fanOutObserver) OnAccept(eventID, listenerID string) {
	f.a.OnAccept(eventID, listenerID)
	f.b.OnAccept(eventID, listenerID)
}

// OnAcceptTiming (bead pg2-n7da9) forwards eventqueue.TimingObserver's hook to
// whichever arms implement it — the metrics emitter feeds its wait/run
// histograms; the activity and listener-count observers have no use for it.
func (f fanOutObserver) OnAcceptTiming(t eventqueue.DispatchTiming) {
	if to, ok := f.a.(eventqueue.TimingObserver); ok {
		to.OnAcceptTiming(t)
	}
	if to, ok := f.b.(eventqueue.TimingObserver); ok {
		to.OnAcceptTiming(t)
	}
}

// OnExpiredPending forwards eventqueue.ExpiredPendingObserver's hook to whichever
// arms implement it; only the metrics emitter counts it.
func (f fanOutObserver) OnExpiredPending(evtType, listenerID string) {
	if o, ok := f.a.(eventqueue.ExpiredPendingObserver); ok {
		o.OnExpiredPending(evtType, listenerID)
	}
	if o, ok := f.b.(eventqueue.ExpiredPendingObserver); ok {
		o.OnExpiredPending(evtType, listenerID)
	}
}

func (f fanOutObserver) OnUnconsumedExpired(evtType string) {
	f.a.OnUnconsumedExpired(evtType)
	f.b.OnUnconsumedExpired(evtType)
}

func (f fanOutObserver) OnDeclined(evtType, listenerID, reason string) {
	f.a.OnDeclined(evtType, listenerID, reason)
	f.b.OnDeclined(evtType, listenerID, reason)
}

func (f fanOutObserver) OnDispatchFailure(evtType, listenerID string) {
	f.a.OnDispatchFailure(evtType, listenerID)
	f.b.OnDispatchFailure(evtType, listenerID)
}

// OnDeduped (Task 2.3, pg2-84o3m.22) deliberately fans out to the activity
// ring ONLY (f.b), never to the metrics emitter (f.a): emitter's own
// OnDeduped is already fed from core.IngestObserver's ingest-only path
// (internal/core/ingest.go, ONE emitter answering both interfaces per this
// function's own doc above) for the exact same event, so also calling it
// here would double-count every ingest-driven dedup in MetricDeduped. This
// queue-level hook additionally covers discover.go's pull path and
// emit.go's push-inject path, which core.IngestObserver's hook never sees at
// all — but that broader coverage stays confined to the activity ring for
// now; widening MetricDeduped's own scope to match is a separate decision.
func (f fanOutObserver) OnDeduped(evtType string) {
	f.b.OnDeduped(evtType)
}

// activityPendingTypesCap bounds activityObserver's eventID->Type
// correlation map (see its doc for why the map exists at all). It is
// independent of the activity.Ring's own capacity: many more events can be
// enqueued-but-not-yet-settled at once than the ring retains outcomes for.
const activityPendingTypesCap = 4096

// activityObserver implements eventqueue.Observer by translating queue
// lifecycle signals into activity.Entry records on a *activity.Ring (Task
// 3.4). It also implements orchestrator.ResourceLimitObserver (this bead,
// pg2-fm2gw) — see OnResourceLimit's own doc below.
//
// Repo-verified discrepancy (this task's curation flag): Entry.Outcome's
// declared vocabulary ("delivered"|"missed"|"rejected"|"declined"|"deduped"|
// "needs_input"|"budget_escalation") is wider than what eventqueue.Observer
// alone can produce. "rejected" comes only from
// core.IngestObserver.OnUnknownTypeRejected; "needs_input" would come from
// roleListener.Offer's internal report, which exposes no such hook on any
// interface today; "deduped" is returned directly from Enqueue's caller,
// never through an Observer callback. "budget_escalation" WAS the same kind
// of gap — Offer's internal report exposed no hook for it either — until
// this bead added orchestrator.ResourceLimitObserver and wired OnResourceLimit
// below; wiring "needs_input" the same way is left to a later phase (or a
// consciously separate task), unchanged by this bead. So this adapter now
// covers five outcomes sourced from eventqueue.Observer (a fourth,
// dispatch_failed, joined the first three at bead pg2-icm3u once
// OnDispatchFailure existed to source it from) plus one, budget_escalation,
// sourced from the separate ResourceLimitObserver hook instead:
//
//	delivered        ≈ OnAccept (unless OnResourceLimit already claimed this eventID — see below)
//	missed           ≈ OnUnconsumedExpired
//	declined         ≈ OnDeclined
//	dispatch_failed  ≈ OnDispatchFailure
//	deduped          ≈ OnDeduped
//	budget_escalation ≈ OnResourceLimit (orchestrator.ResourceLimitObserver, not eventqueue.Observer)
//
// OnAccept(eventID, listenerID string) carries no event TYPE — queue.go's
// own Dispatch has it at the call site (p.evt.Type) but does not thread it
// through the Observer interface; internal/metrics.Emitter hits this exact
// same gap (see its RecordThroughput doc) and defers fixing it the same way.
// Changing eventqueue.Observer's signature is outside this task's Files, so
// this adapter recovers the type itself: OnEnqueue records eventID->Type in
// a small bounded map, and OnAccept consults it. The map is capped at
// activityPendingTypesCap, evicting the oldest insertion once full, so an
// event that is only ever declined-then-expired (retireLocked's
// OnUnconsumedExpired call carries Type directly and needs no lookup, but
// never removes this map's entry either) cannot grow it without bound.
//
// pendingActivity widens that same per-eventID entry (rather than adding a
// second, separately-capped correlation structure) to also carry whether
// THIS accept's own outcome is a resource-limit hit: OnResourceLimit sets it
// INLINE, from inside roleListener.Offer, strictly before Offer returns and
// so strictly before Dispatch's own OnAccept fires for the identical
// eventID (both derive from the one synchronous Offer call — see
// orchestrator.ResourceLimitObserver's own doc) — so OnAccept can render
// "budget_escalation" instead of "delivered" for that one accept with no
// race.
type pendingActivity struct {
	typ           string
	resourceLimit bool
}

type activityObserver struct {
	ring *activity.Ring

	mu      sync.Mutex
	pending map[string]pendingActivity // eventID -> {Type, resource-limit flag}
	order   []string                   // insertion order, for FIFO eviction

	// sourceInFlight tracks, per source name, how many of THIS observer's
	// own OnSourceFetchStart calls have not yet been matched by an
	// OnSourceFetchEnd (DEC-OBS-2, bead pg2-ugcrb) — a COUNT, not a bool,
	// because runAndEnqueue's retry loop re-enters the bracket per attempt
	// (discover.SourceActivityObserver's own doc); a count survives that
	// without ever going negative or reporting "not in flight" between two
	// back-to-back attempts. Guarded by the SAME mu as pending/order above:
	// cardinality is the configured source count (small, static), so one
	// mutex serving both concerns costs nothing extra.
	sourceInFlight map[string]int
}

func NewActivityObserver(ring *activity.Ring) *activityObserver {
	return &activityObserver{ring: ring, pending: make(map[string]pendingActivity), sourceInFlight: make(map[string]int)}
}

func (a *activityObserver) OnEnqueue(evt eventqueue.Event) {
	a.mu.Lock()
	if _, exists := a.pending[evt.ID]; !exists {
		if len(a.order) >= activityPendingTypesCap {
			oldest := a.order[0]
			a.order = a.order[1:]
			delete(a.pending, oldest)
		}
		a.pending[evt.ID] = pendingActivity{typ: evt.Type}
		a.order = append(a.order, evt.ID)
	}
	a.mu.Unlock()
}

// OnRestore implements eventqueue.RestoreObserver (bead pg2-0efop): it seeds
// the eventID->Type correlation for an event restored from the durable queue
// after a restart, so its later OnAccept renders the real type instead of an
// empty one. Unlike OnEnqueue's counterpart nothing is appended to the ring —
// the restored event already had its activity before the restart.
func (a *activityObserver) OnRestore(evt eventqueue.Event) {
	a.OnEnqueue(evt)
}

func (a *activityObserver) OnAccept(eventID, listenerID string) {
	a.mu.Lock()
	p := a.pending[eventID]
	a.mu.Unlock()
	outcome := "delivered"
	if p.resourceLimit {
		outcome = "budget_escalation"
	}
	// Participant (DEC-OBS-2, bead pg2-ugcrb): listenerID was already a
	// parameter here, previously discarded — the accepting listener's own
	// identity is exactly INV-OBS-2's per-listener history key.
	a.ring.Append(activity.Entry{Type: p.typ, Outcome: outcome, Participant: listenerID})
}

// OnResourceLimit implements orchestrator.ResourceLimitObserver (this bead,
// pg2-fm2gw): roleListener.Offer calls it inline, before Offer returns, when
// this dispatch's own outcome was a resource-limit hit (the budget
// watchdog's hard stop) rather than a plain completion. It flags the
// pending entry so the OnAccept notification that always follows a moment
// later for the SAME eventID (Offer's return is unconditionally Accepted
// here) renders "budget_escalation" — the Activity Ring's own pre-existing
// (ADR 0026) vocabulary slot for a component's own resource-limit hit
// (`packages/pg-router/docs/behavior/glossary.md`'s "resource-limit"),
// previously wired to nothing (see this type's own doc above). It upserts
// rather than requiring a prior OnEnqueue hit, so a pending entry evicted by
// activityPendingTypesCap's FIFO cap before this fires still renders
// correctly (just without the FIFO-eviction protection a normal entry gets —
// an accepted imperfection matching this map's existing eviction tolerance).
// This does NOT touch internal/metrics — the operator scope-cut (that
// package's own doc, 2026-07-28) stays exactly as narrow as before; this is
// a separate, ring-only signal.
func (a *activityObserver) OnResourceLimit(eventID, evtType string) {
	a.mu.Lock()
	p := a.pending[eventID]
	p.typ = evtType
	p.resourceLimit = true
	a.pending[eventID] = p
	a.mu.Unlock()
}

func (a *activityObserver) OnUnconsumedExpired(evtType string) {
	a.ring.Append(activity.Entry{Type: evtType, Outcome: "missed"})
}

func (a *activityObserver) OnDeclined(evtType, listenerID, _ string) {
	// Participant (DEC-OBS-2, bead pg2-ugcrb): listenerID was already a
	// parameter here too, previously discarded — see OnAccept's identical
	// note above.
	a.ring.Append(activity.Entry{Type: evtType, Outcome: "declined", Participant: listenerID})
}

func (a *activityObserver) OnDispatchFailure(evtType, _ string) {
	a.ring.Append(activity.Entry{Type: evtType, Outcome: "dispatch_failed"})
}

// OnDeduped (Task 2.3, pg2-84o3m.22) closes the "deduped" gap this type's
// own doc above flagged: eventqueue.Observer now carries the signal
// directly, so it needs no eventID->Type lookup the way OnAccept does.
func (a *activityObserver) OnDeduped(evtType string) {
	a.ring.Append(activity.Entry{Type: evtType, Outcome: "deduped"})
}

// OnSourceFetchStart/OnSourceFetchEnd implement
// discover.SourceActivityObserver's fetch-window bracket (DEC-OBS-2, bead
// pg2-ugcrb): sourceInFlight is a per-source COUNT rather than a bool
// specifically so a retrying source's per-attempt re-entry (discover's own
// doc on this interface) never dips to "not in flight" between two
// back-to-back attempts, and so a stray double-End (which should not
// happen, but costs nothing to guard) cannot drive the count negative.
func (a *activityObserver) OnSourceFetchStart(source string) {
	a.mu.Lock()
	a.sourceInFlight[source]++
	a.mu.Unlock()
}

func (a *activityObserver) OnSourceFetchEnd(source string) {
	a.mu.Lock()
	if a.sourceInFlight[source] > 0 {
		a.sourceInFlight[source]--
	}
	a.mu.Unlock()
}

// InFlightSources implements core.SourceInFlightReader (DEC-OBS-2, bead
// pg2-ugcrb): the names of every source with a positive fetch count right
// now, read live under a.mu — never cached, exactly like
// eventqueue.Queue.InFlightListeners' own read-live posture.
func (a *activityObserver) InFlightSources() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for source, n := range a.sourceInFlight {
		if n > 0 {
			out = append(out, source)
		}
	}
	return out
}

// OnSourceProduced implements discover.SourceActivityObserver's per-pass
// success outcome (DEC-OBS-2, bead pg2-ugcrb): recorded into the SAME
// pool-wide activity ring a handler's own delivered/declined entries go
// into, Participant-tagged with the source's name so a drill-down can
// filter to it — Entry.Type carries no single event type for a whole pass
// (a pass may emit several), so it is left empty here. The Outcome is the
// plain literal "produced" — matching every existing Outcome verb's own
// single-word shape (Entry's doc) — never emitted/rejected embedded into
// the string; those two counts already have a home of their own
// (ProduceReport, and the `sources[]` wire rows), so duplicating them here
// would just be a second, driftable copy for no reader this bead adds.
func (a *activityObserver) OnSourceProduced(source string, _, _ int) {
	a.ring.Append(activity.Entry{Outcome: "produced", Participant: source})
}

// OnSourceGaveUp implements discover.SourceActivityObserver's per-pass
// final-failure outcome (DEC-OBS-2, bead pg2-ugcrb) — the source-side
// analogue of OnDispatchFailure above, Participant-tagged the same way
// OnSourceProduced is.
func (a *activityObserver) OnSourceGaveUp(source string) {
	a.ring.Append(activity.Entry{Outcome: "source_failed", Participant: source})
}

// listenerCountObserver implements eventqueue.Observer to bump a per-role
// delivered/declined tally (Task 4.1 Step 5): the SAME (event, handler)
// acceptance/decline sites INV-EVT-1 already reports via OnAccept/
// OnDeclined — listenerID is the role name (roleListener.ID() returns
// l.role.Name) — fanned into composeStatusReply's listeners[] via
// core.Options.ListenerCounts, the identical *core.ListenerCounts values
// this type writes into. counts is pre-populated (BootCore) for every
// DECLARED role, so a lookup miss (a listenerID this deployment never
// declared) is simply dropped rather than panicking.
type listenerCountObserver struct {
	counts map[string]*core.ListenerCounts
	// now is a clock seam (this task), defaulting to time.Now in
	// newListenerCountObserver -- overridable in tests for a deterministic
	// LastDeliveredAtNanos assertion.
	now func() time.Time
}

func newListenerCountObserver(counts map[string]*core.ListenerCounts) *listenerCountObserver {
	return &listenerCountObserver{counts: counts, now: time.Now}
}

func (l *listenerCountObserver) OnEnqueue(eventqueue.Event) {}

func (l *listenerCountObserver) OnAccept(_, listenerID string) {
	if c := l.counts[listenerID]; c != nil {
		c.Delivered.Add(1)
		c.LastDeliveredAtNanos.Store(l.now().UnixNano())
	}
}

func (l *listenerCountObserver) OnUnconsumedExpired(string) {}

// OnDeclined bumps both the flat Declined tally (unchanged) and, as of bead
// pg2-j4uwg, the SAME role's DeclinedByReason breakdown, keyed by reason —
// eventqueue.Observer's own reason parameter, which by this bead is real,
// additive data rather than discarded (see core.ListenerCounts'
// BumpDeclinedReason doc).
func (l *listenerCountObserver) OnDeclined(_, listenerID, reason string) {
	if c := l.counts[listenerID]; c != nil {
		c.Declined.Add(1)
		c.BumpDeclinedReason(reason)
	}
}

func (l *listenerCountObserver) OnDispatchFailure(string, string) {}

func (l *listenerCountObserver) OnDeduped(string) {}

// handlerFailureCountObserver implements orchestrator.HandlerFailureObserver
// to bump a per-role handler-failure tally (this task), the same pattern
// listenerCountObserver already uses for delivered/declined.
type handlerFailureCountObserver struct {
	counts map[string]*core.ListenerCounts
}

func (h *handlerFailureCountObserver) OnHandlerFailure(_, _, listenerID string, _ error) {
	if c := h.counts[listenerID]; c != nil {
		c.HandlerFailures.Add(1)
	}
}

// fanOutHandlerFailureObserver calls every observer in order -- this task's
// counterpart to eventqueue's own fanOutObserver, needed because
// o.HandlerFailureObserver was previously a single assignment (emitter
// alone), and this task adds a second, independent consumer of the same
// signal.
type fanOutHandlerFailureObserver []orchestrator.HandlerFailureObserver

func (f fanOutHandlerFailureObserver) OnHandlerFailure(eventID, evtType, listenerID string, err error) {
	for _, o := range f {
		o.OnHandlerFailure(eventID, evtType, listenerID, err)
	}
}

// SourceReportsFor builds one core.SourceReport per source in sources —
// cfg.Queries, the post-selector (--only/--disable) active-this-run subset:
// applySelectors already removed any excluded query from the slice, so every
// entry here fired (or was scheduled to fire) this pass. See
// core.TickSnapshot.Sources for the Rejected freedom-boundary note.
//
// rpt is THIS pass's own discover.ProduceReport (Task 4.1), read here only
// for Failure: a source Task 1.3's cadence gating skipped this pass simply
// carries no Failure entry, its documented per-pass scope. lastTick is
// instead the caller's Orchestrator.LastTick() — the merged-forward fire
// history, NOT rpt.LastTick — because unlike Failure, a source's LastTick
// must NOT revert to the zero value on a pass that skipped it (pg2-bzb8i):
// see SourceReport.LastTick's own doc for why a per-pass-only view left the
// operator with no durable positive confirmation a source had ever run.
// Every source here is "pull" (Type/Mode, statusSources' own doc): no push
// query type exists in this codebase today.
func SourceReportsFor(sources query.SourceSet, rpt discover.ProduceReport, lastTick map[string]time.Time) []core.SourceReport {
	if len(sources) == 0 {
		return nil
	}
	out := make([]core.SourceReport, len(sources))
	for i, s := range sources {
		sr := core.SourceReport{Name: s.Name, Type: "pull"}
		if lt, ok := lastTick[s.Name]; ok {
			sr.LastTick = lt
		}
		if f, ok := rpt.Failure[s.Name]; ok {
			sr.Failure = &core.FailureInfo{Count: f.Count, NextEligible: f.NextEligible}
		}
		out[i] = sr
	}
	return out
}

// AttachCalendar fills SourceReport.Calendar for every calendar-triggered
// source in sources: the resolved schedule and next fire from the trigger, the
// last handled slot from the state file (through calstate.Read; an unreadable
// file reads as no state, doctor is where it is reported). reports is
// index-aligned with sources, as SourceReportsFor builds it.
func AttachCalendar(reports []core.SourceReport, sources query.SourceSet, statePath string, now time.Time) {
	var recs map[string]calstate.Record
	read := false
	for i, s := range sources {
		ct, ok := query.Calendar(s.Query.Trigger())
		if !ok || i >= len(reports) {
			continue
		}
		if !read {
			read = true
			if statePath != "" {
				recs, _ = calstate.Read(statePath)
			}
		}
		info := &core.CalendarInfo{Schedule: ct.Describe(), OnMissed: ct.OnMissed, Next: ct.FormatSlot(ct.Next(now))}
		if rec, has := recs[s.Name]; has {
			info.Last = &core.CalendarLast{Slot: ct.FormatSlot(rec.LastSlot), Outcome: rec.Outcome, Reason: rec.Reason}
		}
		reports[i].Calendar = info
	}
}

// calendarStatePath is where the calendar state file lives for cfg.
func calendarStatePath(cfg config.Config) string {
	return filepath.Join(cfg.LogDir, calstate.FileName)
}

// ResolvedConfigFor builds the small resolved-config snapshot
// core.TickSnapshot.Config carries this pass (Task 3.5 Contract; the field
// list itself is this task's own choice — see core.ResolvedConfig's doc).
//
// PollInterval is left nil (omitted) in RunModeDrainAndExit: a one-shot
// drain-to-idle pass has no polling cadence to report, and Task 3.8's status
// IA suppresses tick-derived staleness signals in that mode using this same
// signal [design: Task 3.5 Step 7].
func ResolvedConfigFor(cfg config.Config, runMode string) core.ResolvedConfig {
	rc := core.ResolvedConfig{
		RepoRoot:      cfg.RepoRoot,
		BeadsPrefix:   cfg.BeadsPrefix,
		ActiveRoles:   countEnabledRoles(cfg.Roles),
		ActiveQueries: len(cfg.Queries),
	}
	if runMode == core.RunModeLongRunning {
		pi := cfg.PollInterval
		rc.PollInterval = &pi
	}
	return rc
}

// lateLevelAppender hands the level projection's appends to the queue once the
// queue exists (the projection is built first because the queue is given its
// snapshot accessor at construction).
type lateLevelAppender struct {
	q atomic.Pointer[eventqueue.Queue]
}

// Append implements core.LevelAppender.
func (a *lateLevelAppender) Append(rec core.LevelRecord) (int64, error) {
	q := a.q.Load()
	if q == nil {
		return 0, errors.New("level append before the event queue was built")
	}
	return core.QueueLevelAppender{Q: q}.Append(rec)
}

// levelResources converts the configured resource registry into the projection's
// per-resource ingest configuration.
func levelResources(rs config.ResourceSet) map[string]core.LevelResource {
	out := make(map[string]core.LevelResource, len(rs))
	for _, r := range rs {
		out[r.Name] = core.LevelResource{Sensor: r.Sensor, GraceFactor: r.GraceFactor}
	}
	return out
}

// markNoSensor flags every resource whose sensor is not configured on this
// machine (see config.Config.SensorPresent), so status can say so.
func markNoSensor(out map[string]core.LevelResource, cfg config.Config) map[string]core.LevelResource {
	for name, r := range out {
		r.NoSensor = !cfg.SensorPresent(r.Sensor)
		out[name] = r
	}
	return out
}

// RouterPause returns the router resource's PAUSED level when one is in force:
// its owner and when it was set.
func RouterPause(snap levels.Snapshot) (owner string, since time.Time, ok bool) {
	lv, held := snap.Levels[eventqueue.RouterResource]
	if !held || lv.Value != eventqueue.LevelPaused {
		return "", time.Time{}, false
	}
	return lv.Owner, lv.Since, true
}

// PauseNotice returns the operator-facing stderr notice for a router pause in
// force (INV-LIFE-2): its owner (DEBUG ONLY) and when it was set, plus the
// remedy. Returns "" when the router is not paused. A pause stops ADMISSION only:
// running work is never touched, so the notice says admission, not the whole pool.
func PauseNotice(snap levels.Snapshot) string {
	owner, since, ok := RouterPause(snap)
	if !ok {
		return ""
	}
	p := "pg-router: paused"
	if owner != "" {
		p += " (owner " + owner
		if !since.IsZero() {
			p += ", set " + since.Format(time.RFC3339)
		}
		p += ")"
	}
	return p + " — admission is stopped for every registration (emitters not polled, listeners not dispatched to); resume with `pg-router resume`"
}

// pauseSignature identifies the pause state, so RunOneTick can print the
// operator notice exactly when it changes — a refresh of an unchanged pause
// changes nothing an operator must be told.
func pauseSignature(snap levels.Snapshot) string {
	if owner, _, ok := RouterPause(snap); ok {
		return "router=" + eventqueue.LevelPaused + "/" + owner
	}
	return ""
}

// SourceFailedAttrs builds the slog attributes of the producer-tick "source
// failed" WARN (bead pg2-zdowv): beyond the error it names how long the final
// attempt ran (a value near the scriptout timeout identifies a timeout kill),
// the source's command line, the attempt count and the host's 1-minute load.
// fi is the zero FailureInfo when the failure was not a give-up (a log-limit
// refusal), in which case only the always-known attributes are emitted.
func SourceFailedAttrs(name string, serr error, fi discover.FailureInfo) []any {
	attrs := []any{"source", name, "err", serr}
	if fi.Count > 0 {
		attrs = append(attrs, "elapsed", fi.Elapsed.Round(time.Millisecond), "attempts", fi.Count)
	}
	if len(fi.Argv) > 0 {
		attrs = append(attrs, "argv", strings.Join(fi.Argv, " "))
	}
	if l, ok := Load1(); ok {
		attrs = append(attrs, "load1", l)
	}
	return attrs
}

// RunOneTick executes one iteration of `run`'s drive loop body (INV-LIFE-2):
// it ALWAYS runs ProduceTick + Kick + Expire and publishes the tick — whether
// a level blocks admission is not a pool-wide on/off decided here. Levels act on
// PARTICIPANTS (internal/eventqueue/levelgate.go): ProduceTick skips each emitter
// no consumer of which could run (the timer emitter is never skipped), Kick skips
// each listener whose requirement is unmet, and Expire retires events — so an
// unconstrained participant keeps running while a resource is low, and expiry
// (INV-EVT-4's retry-bound clock) never pauses.
//
// It prints the operator notice to stderr exactly when the router pause CHANGES
// (prevGates is the previous call's return value; the caller's very first call
// passes "" and so reports a pause already in force at startup). The returned
// string is pauseSignature of THIS tick, for the caller to pass back.
//
// q.Dispatch() → q.Kick() (this package's design doc, "decouple
// pg-router-core's tick loop from per-dispatch-pass completion"; INV-LIFE-3,
// docs/behavior/invariants.md): this tick's own ProduceTick/Expire/PublishTick
// sequence no longer waits on any launched offer settling before proceeding to
// the NEXT tick, so one role stuck for its own handler's full MaxWait can no
// longer stall source re-polling or dispatch to every OTHER role.
// INV-CONC-1's one-outstanding-offer-per-handler ceiling is unaffected — that
// lives entirely in Kick's (and Dispatch's) shared phase-1 snapshot.
func RunOneTick(ctx context.Context, cfg config.Config, o *orchestrator.Orchestrator, svc *core.Service, q *eventqueue.Queue, prevGates string, stderr io.Writer) string {
	snap := q.Levels()
	sig := pauseSignature(snap)
	if sig != prevGates {
		if notice := PauseNotice(snap); notice != "" {
			fmt.Fprintln(stderr, notice)
		} else {
			fmt.Fprintln(stderr, "pg-router: not paused — all registrations route normally")
		}
	}
	// The log-size limit controller (bead pg2-5d3ui) runs FIRST, so the
	// emitters-halted decision it derives from the current log size is the one this
	// tick's producer sees, and it runs whether or not producing succeeds.
	q.EnforceLogLimits()
	// Producing, dispatching, expiring and publishing are independent of one
	// another's failure: a ProduceTick error must NOT skip Kick, Expire or
	// PublishTick, or one failing source would stop the very work that frees the
	// log (at the hard limit the loop would wedge for good). rpt is whatever the
	// pass managed to record before it stopped.
	// Load backoff: above cfg.LoadBackoffThreshold the producer pass is skipped
	// (beads list sources were killed at their 40s deadline while load1 was 80 to
	// 140, and each poll is more subprocess load). Everything else in the tick
	// still runs; the skipped sources stay due and are polled by the first tick
	// at or below the threshold.
	var rpt discover.ProduceReport
	var err error
	l, over := LoadBackoff(cfg)
	if o.LoadBackoffObserver != nil {
		o.LoadBackoffObserver.OnProducerTick(over)
	}
	if over {
		slog.Warn("producer tick skipped: host load above the backoff threshold", "load1", l, "threshold", cfg.LoadBackoffThreshold)
	} else {
		rpt, err = o.ProduceTick(ctx, q)
	}
	if err != nil && ctx.Err() != nil {
		// The router's own shutdown cancelled the tick (bead pg2-zdowv,
		// DEC-OBS-8): expected, not a producer failure.
		slog.Info("producer tick interrupted by shutdown", "err", err)
	} else if err != nil {
		slog.Error("producer tick failed; still dispatching, expiring and publishing this tick", "err", err)
	}
	// Source isolation (INV-FAIL-3, INV-EVT-1): a partial produce (one or
	// more SourceErrors) suspends only THAT source's own production —
	// Dispatch/Expire still run over the queue for every other source's
	// and every pushed event's already-queued work.
	for name, serr := range rpt.SourceErrors {
		slog.Warn("producer tick: source failed; other sources still produced", SourceFailedAttrs(name, serr, rpt.Failure[name])...)
	}
	for name, gate := range rpt.Blocked {
		slog.Debug("producer tick: source blocked by a gate; not polled", "source", name, "gate", gate)
	}
	for name, why := range rpt.Halted {
		slog.Debug("producer tick: source not polled; the event log is past its soft size limit or unwritable", "source", name, "state", why)
	}
	for name, n := range rpt.LogRejected {
		slog.Warn("producer tick: events refused; the event log is at its hard size limit or unwritable", "source", name, "events", n)
	}
	// No new offers once shutdown has been requested (a signal landing mid-tick):
	// runRun is about to drain the ones already in flight, and a fresh offer here
	// would start a handler the drain then has to wait for.
	if ctx.Err() == nil {
		q.Kick()
	}
	q.Expire()
	now := time.Now()
	sourceReports := SourceReportsFor(cfg.Queries, rpt, o.LastTick())
	AttachCalendar(sourceReports, cfg.Queries, calendarStatePath(cfg), now)
	svc.PublishTick(core.TickSnapshot{
		Sources:    sourceReports,
		Config:     ResolvedConfigFor(cfg, core.RunModeLongRunning),
		RunMode:    core.RunModeLongRunning,
		Version:    Version,
		LastTickAt: now,
		SnapshotAt: now,
	})
	return sig
}

// BindingInfo lists the (role, event type) pairs of every configured role, the series
// of pg_router_binding_info. A disabled role is included: its type still has a queue
// and the binding is configuration, not liveness (the listener gauges say whether it runs).
func BindingInfo(rs roles.RoleSet) []metrics.Binding {
	var out []metrics.Binding
	for _, r := range rs {
		for _, t := range r.Binds {
			out = append(out, metrics.Binding{Role: r.Name, Type: t})
		}
	}
	return out
}

// countEnabledRoles counts roles active this run (Role.Enabled — selectors.go
// flips this rather than removing entries, so a disabled role's Binds still
// count toward declaredBindTypes, but not toward this active count).
func countEnabledRoles(rs roles.RoleSet) int {
	n := 0
	for _, r := range rs {
		if r.Enabled {
			n++
		}
	}
	return n
}

// Version is the build version the router reports in its metrics build info and
// status snapshots. The binary's main package owns the link-time `-X` target
// (main.version) and assigns it here at start-up.
var Version = "dev"

// RunExclusions is applySelectors' own record of which configured roles/
// queries it excluded THIS RUN (Task 4.1, Binding Decision 4) — captured at
// the exact moment each exclusion decision is made, BEFORE Role.Enabled is
// flipped or a query.Source is dropped: once those mutations run, the
// post-selector config can no longer distinguish "config-disabled" from
// "selector-excluded" (both look like Enabled==false), and an excluded
// query.Source has already vanished from the slice entirely. Consumed by
// BootCore, which threads it into
// core.Options.ExcludedRoles/ExcludedSources so composeStatusReply's
// listeners[]/sources[] can report `excluded` independently of `enabled`.
type RunExclusions struct {
	Roles   []string
	Sources []string
}
