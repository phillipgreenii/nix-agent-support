package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/telemetry"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// idleDrainTick is the between-pass wait runRunUntilIdle's own drive loop
// blocks on while draining (Binding Decision 1: it drives
// eventqueue.Queue.Kick/Expire/Idle directly rather than calling
// eventqueue.Queue.RunUntilIdle, so this wait is a plain time.After rather
// than RunUntilIdle's injectable `after` seam). It is deliberately short and
// unrelated to PollInterval, which paces the PRODUCER's re-query cadence, not
// how fast the queue moves from one already-enqueued head to the next: every
// Listener this binary registers (orchestrator.NewListener) always accepts
// synchronously (INV-CONC-1 — no busy decline), so nothing here is waiting ON
// a handler; the wait only paces how quickly a role's next already-queued
// head gets its turn.
//
// This package's design doc ("decouple pg-router-core's tick loop from
// per-dispatch-pass completion") switched this loop's own dispatch call from
// Dispatch to Kick (INV-LIFE-3, docs/behavior/invariants.md): this tick now
// ALSO covers "wait for a just-launched offer to settle," not only "wait for
// a busy listener to become eligible again" -- an accepted latency tradeoff
// (up to one extra idleDrainTick per drain pass for a fast, non-stuck item)
// in exchange for never blocking the whole drain loop on one genuinely stuck
// listener.
const idleDrainTick = 500 * time.Millisecond

// inFlightDrainTimeout bounds runRun's/runRunUntilIdle's shutdown-ordering
// wait (this package's design doc's "shutdown ordering" note) for every
// Kick()-launched offer still outstanding on q to settle before the durable
// store closes underneath it. By the time this runs, the dispatch context has
// already been cancelled (runRun: after shutdownDrainTimeout's grace; run-
// until-idle: on SIGINT/SIGTERM) -- which is what unsticks a genuinely-stuck
// subprocess call via wireclient's exec.CommandContext -- so this bound only
// needs to cover that unwind's own tail latency, never a fresh MaxWait
// window; a few seconds is ample.
const inFlightDrainTimeout = 5 * time.Second

// shutdownDrainTimeout bounds how long runRun, after SIGINT/SIGTERM, lets
// the dispatches already in flight finish before it cancels their context
// (and, only then, runs the ccpool session sweep in preShutdownAll). Without
// it a pn workspace apply that reloads the launchd agent (~2.5x/day) SIGKILLed
// a running handler ("role desk-pr exited -1") and left a transient pg-desk
// sync_error row.
//
// Sizing evidence (bead pg2-euh4f, from events.jsonl): desk-pr dispatch
// durations have median ~19s, p90 ~31s, p99 ~50s (a loose upper bound); the
// two killed dispatches ran <= 15s. 20s comfortably covers those and the
// median; it does NOT cover the p90 (~31s), which is the price of fitting the
// launchd budget: launchd clamps ExitTimeOut at 60s (pg2-s3fzr, measured),
// so drain (20s) + the 30s session-sweep allowance + inFlightDrainTimeout's
// 5s tail = 55s must stay under the 60s ExitTimeOut in
// darwin/modules/pg-router/default.nix with margin -- keep the two in step,
// and never raise this without shrinking another term. A drain that times out
// is no worse than the old behavior: the dispatch context is then cancelled
// and the handler killed as before.
//
// This budget applies only to roles that do NOT declare survivesShutdown
// (bead pg2-dtigc, ADR 0085). A ccpool-backed role's dispatch lasts far longer
// than any budget launchd allows, so the daemon neither waits for it nor
// cancels it: its handler keeps running and the next daemon adopts the session.
const shutdownDrainTimeout = 20 * time.Second

// inFlightDrainPollInterval is WaitForInFlightDrain's poll cadence during
// the wait inFlightDrainTimeout bounds.
const inFlightDrainPollInterval = 100 * time.Millisecond

// drainThenCloseStore waits (bounded by inFlightDrainTimeout) for every
// Kick()-launched offer still outstanding on q to settle, then closes the
// durable store regardless of whether the wait actually drained everything.
// This ordering is required now that Kick()'s launched goroutines are
// detached from the tick loop: unlike Dispatch(), whose blocking return
// already guaranteed no offer was in flight by the time shutdown began,
// nothing else guarantees a Kick()-launched offer is not still mid-write
// when the store closes underneath it (this package's design doc's
// "shutdown ordering" note).
//
// If the wait times out, the design doc's scenario 5b explicitly requires
// picking one behavior rather than leaving it implicit: this proceeds to
// storeClose() anyway (never blocks shutdown indefinitely) and accepts that
// offer's write is lost, logged at "error" -- matching this package's
// existing swallowed-durable-write precedent (eventqueue's own accept/
// evict-append error handling), never a silent no-op.
//
// survives (nil: none) names listeners whose offers are deliberately left
// running past this process (ADR 0085); they never settle here, so they are
// neither waited on nor reported as lost -- their events stay un-accepted in
// the durable log and the next process re-offers them.
func drainThenCloseStore(q *eventqueue.Queue, storeClose func() error, survives func(listenerID string) bool) {
	drainCtx, cancel := context.WithTimeout(context.Background(), inFlightDrainTimeout)
	defer cancel()
	if !q.WaitForInFlightDrainExcept(drainCtx, inFlightDrainPollInterval, survives) {
		slog.Error("shutdown: in-flight dispatch offer(s) did not drain before the timeout; closing the durable store anyway -- any still-outstanding offer's durable write will be lost",
			"timeout", inFlightDrainTimeout, "sessionsInFlight", q.SessionsInFlight())
	}
	_ = storeClose()
}

// drainThenCancelDispatch is runRun's graceful-shutdown step: it waits (up to
// timeout) for every Kick()-launched offer still in flight on q to finish --
// dispatches run under a context decoupled from the signal context, so they are
// NOT cancelled by SIGTERM -- and then cancels the dispatch context, which
// unsticks any handler that outlived the timeout (wireclient's
// exec.CommandContext kills it). The caller MUST run this before
// preShutdownAll so the ccpool session sweep never races a live dispatch.
//
// survives (nil: none) names the listeners whose dispatches survive a
// shutdown (bead pg2-dtigc, ADR 0085): their listener contexts are not derived
// from the dispatch context, so cancelDispatch does not reach them. They are
// excluded from the wait -- a ccpool-backed dispatch lasts far longer than any
// drain budget launchd allows -- and left running, supervised by their own
// handler, until the next daemon re-offers the event and absorbs the session.
func drainThenCancelDispatch(q *eventqueue.Queue, cancelDispatch context.CancelFunc, timeout time.Duration, survives func(listenerID string) bool) {
	drainCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if !q.WaitForInFlightDrainExcept(drainCtx, inFlightDrainPollInterval, survives) {
		slog.Warn("shutdown: in-flight dispatch(es) did not finish before the drain timeout; cancelling them",
			"timeout", timeout, "sessionsInFlight", q.SessionsInFlight())
	}
	cancelDispatch()
	if survives != nil {
		var left []string
		for lid := range q.InFlightListeners() {
			if survives(lid) {
				left = append(left, lid)
			}
		}
		if len(left) > 0 {
			sort.Strings(left)
			slog.Info("shutdown: leaving dispatch(es) running; the next daemon re-offers their events and adopts the sessions",
				"roles", left)
		}
	}
}

// handlerCommandFor builds the wireclient.CommandFor seam bootCore and
// runRunRole both hand to wireclient.New — the "deployment/wiring layer"
// internal/wireclient's own package doc names as the caller who answers
// "which command does this role's registered handler participant run" (bead
// pg2-g068j resolves the gap both that doc and ADR 0065's Addendum
// forward-reference: docket pg2-oju6w's Task 5.4 wired everything up to this
// seam and deliberately left this seam itself for "a sibling task").
//
// cfg.HandlerCommand (PG_ROUTER_HANDLER_COMMAND) carries no baked-in default
// — see its own doc comment (internal/config/config.go) for why: GOAL-MIN-1's
// Floor forbids this binary's own contract surface from naming a concrete
// tool.
//
// Per-role differentiation (this bead, pg2-ymb3v, closing the gap DEC-WIRE-3
// already anticipated as an accepted shape — "every enabled role resolves to
// the SAME command"): when cfg.HandlerCommandDir is also set, the resolved
// argv threads a per-role --role-config path
// (handlerRoleConfigPath(cfg.HandlerCommandDir, role.Name)) onto the
// handler command, so differently-configured roles sharing one
// HandlerCommand binary (e.g. feedback/worker/review, each with its own
// ccpool actor/prompt) each dispatch through their OWN participant config.
// subcommand is placed as argv[1] — RIGHT AFTER the command, before any
// flags — because the participant's own CLI (e.g.
// pg-router-ccpool-handler/cmd's main.go) parses its first argument as the
// subcommand; wireclient.CommandFor's own doc comment explains why this
// package no longer appends it itself. Falling back to today's
// [cfg.HandlerCommand, subcommand] when HandlerCommandDir is unset keeps an
// existing single-role deployment byte-for-byte unchanged.
func handlerCommandFor(cfg config.Config) wireclient.CommandFor {
	return func(role roles.Role, subcommand string) ([]string, error) {
		if cfg.HandlerCommand == "" {
			return nil, fmt.Errorf("no handler command configured for role %q (set PG_ROUTER_HANDLER_COMMAND)", role.Name)
		}
		if cfg.HandlerCommandDir != "" {
			return []string{cfg.HandlerCommand, subcommand, "--role-config", handlerRoleConfigPath(cfg.HandlerCommandDir, role.Name)}, nil
		}
		return []string{cfg.HandlerCommand, subcommand}, nil
	}
}

// warnHandlerCommandAmbiguity logs a boot-time WARN (this bead, pg2-ymb3v)
// for the silent-misconfiguration shape its own design flagged: more than
// one role ENABLED while only cfg.HandlerCommand (not cfg.HandlerCommandDir)
// is set. handlerCommandFor's fallback branch then resolves every one of
// those roles to the IDENTICAL argv, with no signal that a deployment
// needing per-role differentiation is silently dispatching every role
// through whichever single participant config that one shared command
// happens to point at. A single-enabled-role deployment is unaffected
// (sharing one command with no sibling role to confuse it with is not a
// misconfiguration), which is why this checks countEnabledRoles, not
// len(cfg.Roles).
func warnHandlerCommandAmbiguity(cfg config.Config) {
	if cfg.HandlerCommand == "" || cfg.HandlerCommandDir != "" {
		return
	}
	if n := countEnabledRoles(cfg.Roles); n > 1 {
		slog.Warn("multiple roles enabled but only PG_ROUTER_HANDLER_COMMAND is set; every role will dispatch through the identical handler command with no per-role differentiation — set PG_ROUTER_HANDLER_COMMAND_DIR", "enabledRoles", n)
	}
}

// bootCore loads the durable queue, registers a queue->executor Listener
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
// Matches internal/metrics/metrics_test.go's newHarness circular-construction
// pattern: q is declared (as this function's named return) before New(mp,
// depthFn) closes over it, then constructed for real with WithObserver(emitter).
// mp is resolved by resolveMeterProvider, which defers to cfg.MeterProvider
// when the deployment has bound one (INV-OBS-1: core stays unaware of any
// concrete monitoring backend; binding a real one is a deployment concern
// this function does not take on — it is chosen by CONFIG, not hardcoded
// here, per Task 3.3's binding decision) and otherwise builds a real
// SDK-backed provider with a ManualReader — see resolveMeterProvider's own
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
// declaredRoles is the FULL, pre-selector role list (prepareRun captures it
// before applySelectors runs — Task 4.1, Binding Decision 4) and excluded
// is that same call's runExclusions; both flow straight onto core.Options
// so composeStatusReply's listeners[]/sources[] can render the full
// configured participant set with `excluded` computed independently of
// `enabled` (see core.Options' own docs on DeclaredRoles/ExcludedRoles/
// ExcludedSources).
//
// runMode is core.RunModeLongRunning or core.RunModeDrainAndExit — the same
// two-value identity runOneTick/resolvedConfigFor already thread through
// core.TickSnapshot. bootCore uses it for exactly one decision: whether to
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
func bootCore(ctx context.Context, cfg config.Config, o *orchestrator.Orchestrator, declaredRoles roles.RoleSet, excluded runExclusions, runMode string) (svc *core.Service, q *eventqueue.Queue, mp metric.MeterProvider, storeClose func() error, err error) {
	// o.Handler (this bead, pg2-g068j): wire a real wireclient.Client so
	// dispatch/postStartup/preShutdown reach a registered handler
	// participant instead of falling through to unconfiguredHandler — the
	// "no Handler configured" gap docket pg2-oju6w's Task 5.4 left open (see
	// handlerCommandFor's own doc). Only when unset: a caller (a test's
	// fakeHandlerClient) that pre-set o.Handler before calling bootCore MUST
	// keep its own value, exactly the same "caller wins" pattern o.Cmd/
	// o.Log already follow elsewhere in this package.
	if o.Handler == nil {
		o.Handler = wireclient.New(handlerCommandFor(cfg))
	}
	store, err := eventqueue.NewFileStore(filepath.Join(cfg.LogDir, "queue.jsonl"))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open event queue: %w", err)
	}
	mp, metricsReader := resolveMeterProvider(cfg)
	var metricsOpts []metrics.Option
	if runMode == core.RunModeLongRunning {
		metricsOpts = append(metricsOpts, metrics.WithLiveness(func() bool { return true }))
		if cfg.WorktreeDir != "" {
			// Worktree pool count/size (pg2-kftf9.22): TTL-cached background scan.
			metricsOpts = append(metricsOpts, metrics.WithWorktreePool(cfg.WorktreeDir, 0))
		}
	}
	// Gate Registry gauge (bead pg2-h63eu): one series per gate TYPE in force,
	// read live off the queue on each collect (q is assigned below, before the
	// first collect can run).
	metricsOpts = append(metricsOpts, metrics.WithActiveGates(func() []eventqueue.Gate { return q.ActiveGates() }))
	// Event-queue write-ahead-log size gauge (bead pg2-8e0m6), read live off the
	// queue on each collect.
	metricsOpts = append(metricsOpts, metrics.WithQueueLogSize(func() int64 { return q.LogSize() }))
	// Log-size limit gauges (bead pg2-5d3ui): limit, percent, emitters halted, and
	// which reason (if any) the log is rejecting events for.
	metricsOpts = append(metricsOpts, metrics.WithLogLimitStatus(func() eventqueue.LimitStatus { return q.LimitStatus() }))
	// Per-source last-success / expected-interval gauges (bead pg2-tv11a,
	// DEC-OBS-4): one series per enabled, non-excluded PULL source with a
	// period, last-success initialised to now (process start).
	metricsOpts = append(metricsOpts, metrics.WithPullSources(pullSourceIntervals(cfg)))
	emitter, err := metrics.New(mp, func() map[string]int { return q.DepthByType() }, metricsOpts...)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("construct metrics emitter: %w", err)
	}
	// Wire the same emitter into ProduceTick's pull-source failure retry hook
	// (INV-FAIL-3, register gap R21 / bead pg2-00jpn) — the remaining seam
	// discover.WithSourceFailureObserver's doc left for a follow-on to close.
	o.SourceFailureObserver = emitter
	// ring is the dispatch-outcome activity buffer (Task 3.4): a SECOND
	// eventqueue.Observer, fanned out alongside emitter at this one
	// construction site rather than folded into a new composite-observer
	// abstraction. Task 3.5 is what embeds *ring in the daemon's assembled
	// state for Task 3.8's status verb to read live and directly; this
	// function's own job stops at constructing it and keeping it fed.
	ring := activity.New(cfg.ActivityRingSize)
	// activityObs is the SAME instance wired both as one of the queue's
	// eventqueue.Observer fan-out arms below AND as o.ResourceLimitObserver
	// (this bead, pg2-fm2gw) — it must be the identical instance so
	// roleListener.Offer's inline OnResourceLimit call and the queue's own
	// later OnAccept call land in the SAME correlation map (see
	// activityObserver's own doc). o.ResourceLimitObserver is assigned here,
	// well before the role-registration loop below calls o.NewListener,
	// which captures it onto each roleListener at construction (the same
	// capture-at-construction pattern o.Registry already uses).
	activityObs := newActivityObserver(ring)
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
	q, err = eventqueue.New(store, eventqueue.WithRetryBackoff(cfg.RetryBackoff), eventqueue.WithObserver(fanOutObserver{emitter, fanOutObserver{activityObs, newListenerCountObserver(listenerCounts)}}), eventqueue.WithSerializeTypes(cfg.SerializeTypes...), eventqueue.WithGateObserver(emitter),
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
	// Bindings is built ONCE and threaded into both consumers so they can never
	// disagree about which types are declared (Task 1.1, INV-DISP-3): the same
	// value goes to core.Listen (validates PUSHED events) and onto the
	// Orchestrator (ProduceTick threads it into discover.Produce, which now
	// rejects an undeclared-type PULLED event the same way). declaredBindTypes
	// itself moved to roles.RoleSet.DeclaredBindTypes — a shared home usable by
	// anything holding a RoleSet, not just this binary's bootCore.
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
	}
	if metricsReader != nil {
		// Assigned only when non-nil: metricsReader is a typed *metrics.Reader,
		// and boxing a nil one into the core.MetricsReader interface field
		// unconditionally would make Options.MetricsReader != nil hold true even
		// then (a non-nil interface can wrap a nil pointer) — this guard is what
		// keeps Service.MetricsReader() genuinely nil when resolveMeterProvider
		// returned none (Config.MeterProvider set to an external provider).
		opts.MetricsReader = metricsReader
	}
	svc, err = core.Listen(opts)
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
	// warnHandlerCommandAmbiguity (this bead, pg2-ymb3v): fires alongside the
	// per-role registration loop immediately below, the natural point this
	// run already knows both cfg.Roles' enabled count and the
	// HandlerCommand/HandlerCommandDir configuration it is warning about.
	warnHandlerCommandAmbiguity(cfg)
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
		if runMode == core.RunModeLongRunning && roleSurvivesShutdown(cfg, r.Name) {
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
	return svc, q, mp, store.Close, nil
}

// pullSourceIntervals returns, per configured pull source, the expected
// interval the persistent-failure alert scales its threshold by (bead
// pg2-tv11a, DEC-OBS-4). cfg is the POST-selector config, so a selector-excluded
// source is already absent and is never exported; every configured query is a
// pull source (there is no config-level enable flag for one, and no push query
// type). A source with no period — a manual or threshold trigger with no
// explicit expected_interval — has no cadence to alert against and is omitted.
// A period trigger whose own Every is zero fires on cfg.PollInterval (the same
// fallback discover's cadence uses), so that is its interval.
func pullSourceIntervals(cfg config.Config) map[string]time.Duration {
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

// postStartupAll dispatches handler.postStartup once to every ENABLED
// role's registered handler participant (pg2-oju6w.15), immediately after
// bootCore succeeds in each of runRun/runRunUntilIdle. It
// mirrors bootCore's own registration loop (`for _, r := range cfg.Roles {
// if !r.Enabled { continue } ...}`), never declaredRoles (the full
// pre-selector superset used only for status reporting). Built for symmetry
// with preShutdownAll (decision #2 — nothing consumes postStartup's outcome
// today); a hook failure is logged and does not abort boot.
//
// A nil o.Handler is guarded explicitly rather than left to panic on a nil
// interface call: bootCore now always wires a real wireclient.Client (this
// bead, pg2-g068j, closing the CommandFor/bootCore gap Task 5.4 left open),
// so this guard only still fires for a caller that builds its own
// Orchestrator without going through bootCore at all — an unwired Handler is
// exactly the same class of problem as a per-call error, so it is logged
// once per role and skipped, not fatal.
func postStartupAll(ctx context.Context, o *orchestrator.Orchestrator, cfg config.Config) {
	if o.Handler == nil {
		slog.Warn("postStartup skipped: no Handler configured (internal/wireclient.HandlerClient)")
		return
	}
	for _, r := range cfg.Roles {
		if !r.Enabled {
			continue
		}
		if _, err := o.Handler.PostStartup(ctx, r); err != nil {
			slog.Warn("postStartup failed", "role", r.Name, "err", err)
		}
	}
}

// preShutdownAll dispatches handler.preShutdown ONCE per daemon shutdown —
// to the first ENABLED role's registered handler participant only — at the
// same point TeardownAll used to fire (pg2-oju6w.15).
//
// SUPERSEDES pg2-oju6w.15's original "dispatch once per enabled role"
// shape (bead pg2-asr8z, 2026-09-22): that shape called this hook once per
// enabled role, on the theory (recorded in
// pg-router-ccpool-handler/cmd/pg-router-ccpool-handler/preshutdown.go's own
// runPreShutdown doc, "decision #1") that N redundant sweeps were merely
// harmless waste. Production evidence (pg2-1kq1x's verification, root-caused
// as pg2-asr8z) proved that framing wrong: with N enabled roles, this used
// to launch N sequential fresh "preShutdown" SUBPROCESSES, and every one of
// them ran its own full ccpool-list-and-close sweep (measured ~0.74s per
// `ccpool list --all` alone) — sequentially eating enough of launchd's 5s
// ExitTimeOut that the daemon was SIGKILLed before even the FIRST sweep
// finished, let alone the rest. Calling it once is safe because that sweep
// is GLOBAL and invariant across roles: it keys only on the handler
// process's own launch config (PG_ROUTER_CCPOOL_HANDLER_CONFIG, inherited
// unchanged by every subprocess spawn) and never reads the per-role
// --role-config path at all — so every enabled role's call would have
// triggered the byte-for-byte identical sweep anyway. A hook failure is
// logged and MUST NOT crash shutdown. See postStartupAll's doc for the
// nil-Handler guard's own rationale — postStartupAll itself is UNCHANGED
// and out of scope for pg2-asr8z (nothing consumes its outcome today, so it
// carries none of preShutdown's timeout risk).
func preShutdownAll(ctx context.Context, o *orchestrator.Orchestrator, cfg config.Config) {
	if o.Handler == nil {
		slog.Warn("preShutdown skipped: no Handler configured (internal/wireclient.HandlerClient)")
		return
	}
	for _, r := range cfg.Roles {
		if !r.Enabled {
			continue
		}
		if _, err := o.Handler.PreShutdown(ctx, r); err != nil {
			slog.Warn("preShutdown failed", "role", r.Name, "err", err)
		}
		// Dedup (pg2-asr8z): dispatch to exactly one enabled role's handler
		// participant, never to every one of them — see this function's own
		// doc above for why calling it more than once is redundant, not
		// merely harmless.
		return
	}
}

// resolveMeterProvider decides the MeterProvider metrics.New registers the
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
func resolveMeterProvider(cfg config.Config) (mp metric.MeterProvider, metricsReader *metrics.Reader) {
	if cfg.MeterProvider != nil {
		return cfg.MeterProvider, nil
	}
	return metrics.NewReadableProvider()
}

// monitorSubsetResolverFrom adapts cfg.MonitorSubsets (a plain map, Task
// 3.6-prereq) into the core.MonitorSubsetResolver function core.Options
// wants — the same config->core.Options adaptation bootCore already
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
// ring at bootCore's one construction site (eventqueue.WithObserver keeps
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

func newActivityObserver(ring *activity.Ring) *activityObserver {
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
// this type writes into. counts is pre-populated (bootCore) for every
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

// preparedRun is the config/precheck/eventlog setup shared by `run` and
// `run-until-idle` — the same setup runDrain used to do before this bead
// retired it.
type preparedRun struct {
	cfg     config.Config
	o       *orchestrator.Orchestrator
	cleanup func() // closes the eventlog writer, if one was opened
	// declaredRoles/excluded are Task 4.1's own captures (Binding Decision
	// 4): the FULL, pre-selector role list and the run-scoped exclusions
	// applySelectors computed at its own decision point — both threaded
	// into bootCore's core.Options wiring by every call site below.
	declaredRoles roles.RoleSet
	excluded      runExclusions
}

// prepareRun loads config, warns on stale env / tracked config / stub queries /
// stranded feedback, applies this run's --only/--disable selectors
// (STORY-OP-3), and wires the orchestrator + its event log — everything
// `run` / `run-until-idle` need before they may touch the queue or the core.
// On failure it prints the same diagnostic runDrain used to and returns a
// non-OK exit code; the caller MUST check code before using the returned
// preparedRun.
//
// bd-reachability/prefix precheck and self_login resolution no longer run
// here (Task 5.8, ADR 0065's "Source-side boundary" section): both moved to
// the new pg-router-ccpool-handler module's own startup pre-flight
// (cmd/pg-router-ccpool-handler/preflight.go) — pg-router's own pre-runtime
// validation is exactly INV-WORKFLOW-1's six determinable conditions,
// unrelated to bd/self_login.
//
// sel's selectors are applied AFTER config.Load() (whose own preamble already
// calls cfg.Validate() — internal/config/config.go) deliberately, never
// before: applySelectors' own doc comment (selectors.go) explains why a
// re-Validate against the run-scoped subset would produce false findings —
// nothing past this point may call Validate() again.
func prepareRun(ctx context.Context, sel runSelectors) (preparedRun, int) {
	// Fan the default slog logger out to the OTLP bridge (design section 5.2):
	// stderr output is kept and every record from this point on — including
	// the "starting" line immediately below and every WARN/ERROR the rest of
	// this run emits — is ALSO pushed over OTLP once Init installed a real
	// LoggerProvider (a no-op elsewhere, so this is cheap and safe to do
	// unconditionally). Applied here, shared by both run and run-until-idle,
	// rather than only inside runRun: the design's "daemon mode" scoping is
	// stated for the METRICS exporter specifically (section 4), not for logs
	// (section 5), and run-until-idle's periodic drain pass emits the same
	// class of operational WARN/ERROR lines a long-running run does.
	//
	// The stderr side MUST be a freshly-constructed slog.TextHandler, never
	// slog.Default().Handler(): before any SetDefault call, that accessor
	// returns slog's internal defaultHandler shim, which delegates through
	// the legacy log package's own Output — and per Go's log/slog
	// interoperability (log.Logger writing through an slog.handlerWriter),
	// wrapping THAT shim in a Fanout and then calling slog.SetDefault(...)
	// with it creates a self-referential cycle: the shim calls back into the
	// very slog.Default() it is now part of, deadlocking on the legacy log
	// package's own non-reentrant mutex on the SECOND recursive entry
	// (reproduced live: run/run-until-idle hung forever the moment
	// config.Load()'s first slog.Info call fired, confirmed via a SIGQUIT
	// goroutine dump). pg-pr's own internal/sync/daemon.go sidesteps this
	// the same way (NewTextHandler/NewJSONHandler build a handler directly,
	// never touch slog.Default()) — mirrored here rather than rediscovered.
	slog.SetDefault(slog.New(telemetry.Fanout(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
		telemetry.NewSlogHandler(),
	)))

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return preparedRun{}, exitPrecheck
	}
	warnDroppedRoleEnv()
	warnTrackedConfig(ctx, cfg)
	warnStubQueries(cfg)

	slog.Info("starting", "repo", cfg.RepoRoot, "config", cfg.ConfigPath, "roles", len(cfg.Roles))

	// declaredRoles captures the FULL, pre-selector role list (Task 4.1,
	// Binding Decision 4) BEFORE applySelectors runs: applySelectors
	// copies cfg.Roles into a fresh backing array before flipping Enabled
	// on any excluded entry, so this slice header stays valid and
	// unmutated by that later call.
	declaredRoles := cfg.Roles
	cfg, excluded, err := applySelectors(cfg, sel)
	if err != nil {
		printUsageErr(fmt.Sprintf("selector: %v", err))
		return preparedRun{}, exitUsage
	}
	activeRoles := 0
	for _, r := range cfg.Roles {
		if r.Enabled {
			activeRoles++
		}
	}
	slog.Info("run-scoped selectors applied", "only", sel.Only, "disable", sel.Disable,
		"active roles", activeRoles, "total roles", len(cfg.Roles), "active queries", len(cfg.Queries))

	o := &orchestrator.Orchestrator{
		Reg: cfg.Roles,
		Cfg: cfg,
	}
	cleanup := func() {}
	if lw, err := eventlog.New(filepath.Join(cfg.LogDir, "events.jsonl")); err != nil {
		slog.Warn("eventlog unavailable; watchdog events will not be written", "err", err)
	} else {
		o.Log = lw
		cleanup = func() { _ = lw.Close() }
	}
	return preparedRun{cfg: cfg, o: o, cleanup: cleanup, declaredRoles: declaredRoles, excluded: excluded}, exitOK
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

// sourceReportsFor builds one core.SourceReport per source in sources —
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
func sourceReportsFor(sources query.SourceSet, rpt discover.ProduceReport, lastTick map[string]time.Time) []core.SourceReport {
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

// resolvedConfigFor builds the small resolved-config snapshot
// core.TickSnapshot.Config carries this pass (Task 3.5 Contract; the field
// list itself is this task's own choice — see core.ResolvedConfig's doc).
//
// PollInterval is left nil (omitted) in RunModeDrainAndExit: a one-shot
// drain-to-idle pass has no polling cadence to report, and Task 3.8's status
// IA suppresses tick-derived staleness signals in that mode using this same
// signal [design: Task 3.5 Step 7].
func resolvedConfigFor(cfg config.Config, runMode string) core.ResolvedConfig {
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

// gateNotice returns the operator-facing stderr notice for the gates currently
// in force (INV-LIFE-2, Gate Registry bead pg2-h63eu): one entry per gate with
// its TYPE, owner (DEBUG ONLY) and when it was set, plus the remedy. Returns ""
// when no gate is active. A gate acts on the participants that block on it, not
// on the whole pool, so the notice says "participants that block on them" rather
// than claiming everything is halted.
func gateNotice(gates []eventqueue.Gate) string {
	if len(gates) == 0 {
		return ""
	}
	parts := make([]string, 0, len(gates))
	for _, g := range gates {
		p := g.Type
		if g.Owner != "" {
			p += " (owner " + g.Owner + ", "
		} else {
			p += " ("
		}
		p += "set " + g.SetAt.Format(time.RFC3339)
		if !g.ExpiresAt.IsZero() {
			p += ", lease until " + g.ExpiresAt.Format(time.RFC3339)
		}
		parts = append(parts, p+")")
	}
	return "pg-router: gated by " + strings.Join(parts, "; ") +
		" — participants that block on them are halted (emitters not polled, listeners not dispatched to); " +
		"clear with `pg-router gate clear <TYPE>` (or `pg-router resume --all`)"
}

// gateSignature identifies the SET of active gate TYPEs, so runOneTick can
// print the operator notice exactly when that set changes — a lease renewal (a
// re-set of an already-active TYPE) changes nothing an operator must be told.
func gateSignature(gates []eventqueue.Gate) string {
	types := make([]string, len(gates))
	for i, g := range gates {
		types[i] = g.Type
	}
	return strings.Join(types, ",")
}

// sourceFailedAttrs builds the slog attributes of the producer-tick "source
// failed" WARN (bead pg2-zdowv): beyond the error it names how long the final
// attempt ran (a value near the scriptout timeout identifies a timeout kill),
// the source's command line, the attempt count and the host's 1-minute load.
// fi is the zero FailureInfo when the failure was not a give-up (a log-limit
// refusal), in which case only the always-known attributes are emitted.
func sourceFailedAttrs(name string, serr error, fi discover.FailureInfo) []any {
	attrs := []any{"source", name, "err", serr}
	if fi.Count > 0 {
		attrs = append(attrs, "elapsed", fi.Elapsed.Round(time.Millisecond), "attempts", fi.Count)
	}
	if len(fi.Argv) > 0 {
		attrs = append(attrs, "argv", strings.Join(fi.Argv, " "))
	}
	if l, ok := load1(); ok {
		attrs = append(attrs, "load1", l)
	}
	return attrs
}

// runOneTick executes one iteration of `run`'s drive loop body (INV-LIFE-2):
// it ALWAYS runs ProduceTick + Kick + Expire and publishes the tick — whether
// a gate is active is no longer a pool-wide on/off decided here. Under the Gate
// Registry (bead pg2-h63eu, internal/eventqueue/gate.go) a gate acts on
// PARTICIPANTS: ProduceTick skips each emitter that blocks on an active gate
// (the timer emitter is never skipped), Kick skips each listener that does
// (gate records themselves are still delivered), and Expire sweeps lapsed gate
// leases and retires events — so an exempt participant keeps running while a
// gate is up, and expiry (INV-EVT-4's retry-bound clock) never pauses.
//
// It prints the operator notice to stderr exactly when the set of active gate
// TYPEs CHANGES (prevGates is the previous call's return value; the caller's
// very first call passes "" and so reports a gate already active at startup).
// The returned string is gateSignature of THIS tick, for the caller to pass back.
//
// q.Dispatch() → q.Kick() (this package's design doc, "decouple
// pg-router-core's tick loop from per-dispatch-pass completion"; INV-LIFE-3,
// docs/behavior/invariants.md): this tick's own ProduceTick/Expire/PublishTick
// sequence no longer waits on any launched offer settling before proceeding to
// the NEXT tick, so one role stuck for its own handler's full MaxWait can no
// longer stall source re-polling or dispatch to every OTHER role.
// INV-CONC-1's one-outstanding-offer-per-handler ceiling is unaffected — that
// lives entirely in Kick's (and Dispatch's) shared phase-1 snapshot.
func runOneTick(ctx context.Context, cfg config.Config, o *orchestrator.Orchestrator, svc *core.Service, q *eventqueue.Queue, prevGates string, stderr io.Writer) string {
	gates := q.ActiveGates()
	sig := gateSignature(gates)
	if sig != prevGates {
		if notice := gateNotice(gates); notice != "" {
			fmt.Fprintln(stderr, notice)
		} else {
			fmt.Fprintln(stderr, "pg-router: no gate active — all participants route normally")
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
	rpt, err := o.ProduceTick(ctx, q)
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
		slog.Warn("producer tick: source failed; other sources still produced", sourceFailedAttrs(name, serr, rpt.Failure[name])...)
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
	svc.PublishTick(core.TickSnapshot{
		Sources:    sourceReportsFor(cfg.Queries, rpt, o.LastTick()),
		Config:     resolvedConfigFor(cfg, core.RunModeLongRunning),
		RunMode:    core.RunModeLongRunning,
		Version:    version,
		LastTickAt: now,
		SnapshotAt: now,
	})
	return sig
}

// runRunUntilIdle implements `pg-router run-until-idle` (and the deprecated
// `drain` alias): boot the core, fire ONE producer tick (matching the single
// discovery pass a `drain` invocation used to run), drain the durable queue
// until it is idle (INV-LIFE-1's drain-and-exit mode: every enqueued event
// accepted or expired, no offer outstanding), then close the core and tear
// down every pg-router-* session. It never touches internal/eventbus or a
// per-role Cap — both retired by this bead (pg2-f3mcb.2).
//
// only/disable are this invocation's --only/--disable flag occurrences
// (STORY-OP-3, DEC-CLI-1), as parsed by parseRunLikeArgs; resolveSelectors
// folds in PG_ROUTER_ONLY/PG_ROUTER_DISABLE before prepareRun applies them.
func runRunUntilIdle(only, disable []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pr, code := prepareRun(ctx, resolveSelectors(only, disable))
	if code != exitOK {
		return code
	}
	defer pr.cleanup()
	return runUntilIdleBody(ctx, pr)
}

// runUntilIdleBody is runRunUntilIdle's boot-to-exit body over an already
// prepared run, split out so a test can drive it with a hand-built
// preparedRun (no env/config resolution, no signal handling).
func runUntilIdleBody(ctx context.Context, pr preparedRun) int {
	svc, q, mp, storeClose, err := bootCore(ctx, pr.cfg, pr.o, pr.declaredRoles, pr.excluded, core.RunModeDrainAndExit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-until-idle:", err)
		return exitGeneric
	}
	postStartupAll(ctx, pr.o, pr.cfg)
	// Ordering considered and accepted (review of this bead's design doc,
	// "decouple pg-router-core's tick loop from per-dispatch-pass
	// completion"): defers run LIFO, and metrics.Flush's defer below is
	// registered AFTER this one, so it runs BEFORE drainThenCloseStore's
	// in-flight wait completes. A Kick()-launched offer that is still
	// settling at shutdown can therefore be invisible to the final metrics
	// snapshot. This is judged low-severity — an inherent limitation of any
	// "final snapshot" taken before every last write is guaranteed
	// flushed — and pre-existing in shape (a short run finishing between two
	// periodic collections had the same blind spot before Kick() existed),
	// not a regression this decoupling introduces. Left unreordered
	// deliberately: reordering would make metrics.Flush wait on the SAME
	// bounded drain this defer performs, which is a real behavior change,
	// not a comment-only fix.
	defer drainThenCloseStore(q, storeClose, nil)
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()

	defer preShutdownAll(context.Background(), pr.o, pr.cfg)
	defer func() {
		_ = svc.Close()
		if err := <-accepted; err != nil {
			slog.Warn("core accept loop exited with an error", "err", err)
		}
	}()
	// Force a final metrics snapshot before exit, on EVERY exit path below
	// (a defer, not a single call before the happy-path return): without
	// this, a short run that starts and finishes between two periodic
	// collections of a REAL backend would report nothing (the no-op default
	// has nothing to flush regardless), and a ProduceTick/RunUntilIdle
	// failure used to skip it entirely.
	//
	// Ordering considered and accepted (see drainThenCloseStore's defer
	// above, registered first): this defer runs BEFORE that one (LIFO), so
	// this snapshot can miss the very last accept settling during
	// drainThenCloseStore's own in-flight wait. Accepted as a pre-existing
	// final-snapshot limitation, not a new regression — see that defer's own
	// comment for the full reasoning.
	defer func() {
		if err := metrics.Flush(context.Background(), mp); err != nil {
			slog.Warn("run-until-idle: metrics flush failed", "err", err)
		}
	}()

	// INV-LIFE-2's gated drain-and-exit slice (Gate Registry, bead pg2-h63eu):
	// gates live in the event log, so they are only knowable once the core is
	// booted. With ANY gate active this run still boots (above) and stays
	// reachable — a concurrent `ingest-event` push still succeeds and is
	// durably enqueued — but it never produces or drains: a gate suspends the
	// participants that block on it, so a drain-to-idle could spin on events a
	// blocked listener will not take until the gate clears. The reachability
	// window is bounded to ONE idleDrainTick pass; expiry still runs once
	// (INV-EVT-4's clock does not pause), and the final metrics snapshot is
	// still flushed by the defers above. It MUST NOT report the queue as
	// drained — it never drained anything.
	if gates := q.ActiveGates(); len(gates) > 0 {
		fmt.Fprintln(os.Stderr, gateNotice(gates))
		q.Expire()
		select {
		case <-ctx.Done():
		case <-time.After(idleDrainTick):
		}
		slog.Info("run-until-idle: gated; skipped drain (no dispatch), expiry advanced")
		return exitOK
	}

	q.EnforceLogLimits()
	rpt, produceErr := pr.o.ProduceTick(ctx, q)
	if produceErr != nil {
		// Do not exit before draining (bead pg2-5d3ui): the drain below is what frees
		// the log, and a pass that failed part-way left already-queued work that is
		// owed its delivery opportunity (INV-EVT-1). The run still exits generic-
		// failure once it has drained.
		fmt.Fprintln(os.Stderr, "run-until-idle: discover:", produceErr)
	}
	// Binding Decision 1: drive Kick/Expire/Idle directly rather than
	// calling eventqueue.Queue.RunUntilIdle, so a tick snapshot can be
	// published after every pass. RunUntilIdle itself (queue.go) is left
	// completely unmodified — its own doc comment explains why dispatch runs
	// before expire, which this loop preserves.
	//
	// q.Dispatch() → q.Kick() (this package's design doc, "decouple
	// pg-router-core's tick loop from per-dispatch-pass completion";
	// INV-LIFE-3, docs/behavior/invariants.md): draining toward idle must
	// maximize throughput, never spend wall-clock time blocked on one stuck
	// listener while others have deliverable work. q.Idle() below is
	// unchanged and already correct against Kick's contract: headFor skips a
	// (listener, event) pair only once it is SETTLED (e.settled), never on
	// q.inFlight/busy-ness, so a listener with a still-unsettled
	// Kick()-launched offer keeps reporting that same head as its
	// deliverable one — Idle() sees it and correctly returns false — until
	// that offer's own phase 3 actually settles it.
	for {
		q.EnforceLogLimits()
		q.Kick()
		q.Expire()
		now := time.Now()
		svc.PublishTick(core.TickSnapshot{
			Sources:    sourceReportsFor(pr.cfg.Queries, rpt, pr.o.LastTick()),
			Config:     resolvedConfigFor(pr.cfg, core.RunModeDrainAndExit),
			RunMode:    core.RunModeDrainAndExit,
			Version:    version,
			LastTickAt: now,
			SnapshotAt: now,
		})
		if q.Idle() {
			break
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "run-until-idle:", ctx.Err())
			return exitGeneric
		case <-time.After(idleDrainTick):
		}
	}
	// Source isolation (INV-FAIL-3, INV-EVT-1; ADR per Task 0.6 — INV-PREC-1
	// resolved as never-drop-work): the drain above already completed — every
	// enqueued event (including one pushed in over the socket, unrelated to any
	// failing pull source) was dispatched or expired — regardless of a partial
	// produce. Only NOW, after that drain, does a partial produce make this run
	// exit generic-failure (1): deliberately not a branchable, specific code
	// (repo's coarse exit-code convention, ADR 0042) — a caller MUST NOT infer
	// more from it than "something did not fully succeed."
	if len(rpt.SourceErrors) > 0 || produceErr != nil {
		for name, serr := range rpt.SourceErrors {
			slog.Error("run-until-idle: source failed; other sources still drained", "source", name, "err", serr)
		}
		slog.Info("run-until-idle: queue drained (partial produce)")
		return exitGeneric
	}
	slog.Info("run-until-idle: queue drained")
	return exitOK
}

// resolveMetricsAddr decides the --metrics-addr this invocation actually
// uses: the flag occurrence, else cfg.MetricsAddr (PG_ROUTER_METRICS_ADDR),
// else "" (disabled) — CLI flag > env > built-in default, the same
// precedence PG_ROUTER_TUI_INTERVAL already documents in args.go. Split out
// as a pure function (no I/O) so the precedence itself is unit-testable
// without booting a real metrics HTTP listener.
func resolveMetricsAddr(flagAddr string, cfg config.Config) string {
	if flagAddr != "" {
		return flagAddr
	}
	return cfg.MetricsAddr
}

// runRun implements `pg-router run`: boot the core and run indefinitely,
// producing and dispatching on cfg.PollInterval, until SIGINT/SIGTERM requests
// an orderly shutdown (INV-LIFE-1's daemon mode). It stays reachable to push
// participants throughout — the socket Accept loop runs the whole time,
// sharing the SAME *eventqueue.Queue the produce/dispatch loop drives, so a
// push arriving mid-tick is picked up on the next dispatch.
//
// only/disable are this invocation's --only/--disable flag occurrences
// (STORY-OP-3, DEC-CLI-1); see runRunUntilIdle's doc comment. metricsAddr is
// this invocation's --metrics-addr occurrence (design section 4): empty
// (the default) leaves resolveMeterProvider's existing package-default
// ManualReader provider in place, unchanged; a non-empty value starts the
// OTel Prometheus /metrics HTTP endpoint (metrics_http.go's
// startMetricsServer) and binds ITS MeterProvider onto pr.cfg BEFORE
// bootCore runs, so resolveMeterProvider's cfg.MeterProvider-set branch —
// the "deployment-bound-backend" case its own doc comment already
// anticipated — resolves to it.
func runRun(only, disable []string, metricsAddr string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pr, code := prepareRun(ctx, resolveSelectors(only, disable))
	if code != exitOK {
		return code
	}
	defer pr.cleanup()

	if metricsAddr = resolveMetricsAddr(metricsAddr, pr.cfg); metricsAddr != "" {
		metricsMP, metricsShutdown, err := startMetricsServer(metricsAddr, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "run: metrics server:", err)
			return exitGeneric
		}
		pr.cfg.MeterProvider = metricsMP
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsShutdown(shutdownCtx); err != nil {
				slog.Warn("run: metrics server shutdown failed", "err", err)
			}
		}()
	}

	return runLongRunningBody(ctx, pr, shutdownDrainTimeout)
}

// runLongRunningBody is runRun's boot-to-exit body over an already prepared
// run, split out (like runUntilIdleBody) so a test can drive it with a
// hand-built preparedRun and an injectable drainTimeout. ctx is the SIGNAL
// context: it stops the tick loop and the core accept loop. Dispatches run
// under a separate dispatchCtx that is cancelled only after the shutdown drain
// (drainThenCancelDispatch), so a SIGTERM lets in-flight handlers finish
// instead of SIGKILLing them (bead pg2-euh4f).
func runLongRunningBody(ctx context.Context, pr preparedRun, drainTimeout time.Duration) int {
	dispatchCtx, cancelDispatch := context.WithCancel(context.Background())
	defer cancelDispatch()
	svc, q, mp, storeClose, err := bootCore(dispatchCtx, pr.cfg, pr.o, pr.declaredRoles, pr.excluded, core.RunModeLongRunning)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return exitGeneric
	}
	postStartupAll(ctx, pr.o, pr.cfg)
	// Ordering considered and accepted (same reasoning as runRunUntilIdle's
	// identical defer pair; review of this bead's design doc, "decouple
	// pg-router-core's tick loop from per-dispatch-pass completion"): defers
	// run LIFO, and metrics.Flush's defer below is registered AFTER this
	// one, so it runs BEFORE drainThenCloseStore's in-flight wait completes
	// — the very last accept settling during shutdown can be invisible to
	// the final metrics snapshot. Judged low-severity (an inherent
	// "final snapshot" limitation, not a regression this decoupling
	// introduces) and left unreordered deliberately: reordering would make
	// metrics.Flush block on this same drain, a real behavior change, not a
	// comment-only fix.
	// survives names the roles whose dispatches this shutdown leaves running
	// (ADR 0085); the drain and the store close do not wait on them.
	survivors := survivingRoles(pr.cfg)
	survives := func(listenerID string) bool { return survivors[listenerID] }
	defer drainThenCloseStore(q, storeClose, survives)
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()

	defer preShutdownAll(context.Background(), pr.o, pr.cfg)
	defer func() {
		_ = svc.Close()
		if err := <-accepted; err != nil {
			slog.Warn("core accept loop exited with an error", "err", err)
		}
	}()
	// Force a final metrics snapshot/export before exit (mirrors
	// runRunUntilIdle's own identical defer): without this, a
	// --metrics-addr-configured Prometheus exporter's periodic collection
	// tick might never land before shutdown, and the package-default
	// ManualReader has nothing to flush regardless (a safe no-op either way
	// — see metrics.Flush's own doc comment).
	//
	// Ordering considered and accepted (see drainThenCloseStore's defer
	// above, registered first): this defer runs BEFORE that one (LIFO), so
	// this snapshot can miss the very last accept settling during
	// drainThenCloseStore's own in-flight wait. Accepted as a pre-existing
	// final-snapshot limitation, not a new regression — see that defer's own
	// comment for the full reasoning.
	defer func() {
		if err := metrics.Flush(context.Background(), mp); err != nil {
			slog.Warn("run: metrics flush failed", "err", err)
		}
	}()

	tick := pr.cfg.PollInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	prevGates := ""
	for {
		prevGates = runOneTick(ctx, pr.cfg, pr.o, svc, q, prevGates, os.Stderr)
		select {
		case <-ctx.Done():
			slog.Info("run: shutdown requested")
			// Drain BEFORE returning: the deferred preShutdownAll (the ccpool
			// session sweep) must not run while a dispatch is still in flight.
			drainThenCancelDispatch(q, cancelDispatch, drainTimeout, survives)
			return exitOK
		case <-ticker.C:
		}
	}
}
