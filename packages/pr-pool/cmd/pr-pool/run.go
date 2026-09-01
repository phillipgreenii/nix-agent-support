package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/metrics"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// idleDrainTick is the between-pass wait eventqueue.Queue.RunUntilIdle blocks
// on while draining (`run-until-idle`). It is deliberately short and unrelated
// to PollInterval, which paces the PRODUCER's re-query cadence, not how fast
// the queue moves from one already-enqueued head to the next: every Listener
// this binary registers (orchestrator.NewListener) always accepts synchronously
// (INV-CONC-1 — no busy decline), so nothing here is waiting ON a handler; the
// wait only paces how quickly a role's next already-queued head gets its turn.
const idleDrainTick = 500 * time.Millisecond

// bootCore loads the durable queue, registers a queue->executor Listener
// (orchestrator.NewListener) for every ENABLED role, and starts the core socket
// service (internal/core.Listen) — the run/run-until-idle wiring bead pg2-f3mcb.2
// adds. Before this, nothing outside internal/core's own tests called
// Listen+Accept; production `drain` ran the retired internal/eventbus +
// internal/orchestrator.DrainOnce path instead.
//
// It also wires ONE metrics.Emitter into both production seams that can drive
// it — eventqueue.WithObserver at queue construction and core.Options.Observer
// at Listen — so a single emitter answers both eventqueue.Observer and
// core.IngestObserver, matching internal/metrics/metrics_test.go's newHarness
// circular-construction pattern: q is declared (as this function's named
// return) before New(mp, depthFn) closes over it, then constructed for real
// with WithObserver(emitter). mp is resolved from cfg.Meter(), which defaults
// to the OTel no-op provider when cfg.MeterProvider is unset (INV-OBS-1: core
// stays unaware of any concrete monitoring backend; binding a real one is a
// deployment concern this function does not take on — it is chosen by
// CONFIG, not hardcoded here, per Task 3.3's binding decision).
//
// The returned storeClose MUST be deferred by the caller: eventqueue.Queue owns
// no Close of its own (Store is an injected seam), so the file handle beneath it
// is this function's caller's to release.
//
// cfg.SerializeTypes (INV-CONC-1, `packages/pr-pool/docs/decisions · DEC-CONC-1`)
// threads through as eventqueue.WithSerializeTypes the same way cfg.RetryBackoff
// threads through as WithRetryBackoff — this is the ONE production seam that
// resolves the config-level mark into the queue's dispatch-time occupancy gate;
// an empty/absent [pool].serialize_types leaves every type unaffected.
func bootCore(ctx context.Context, cfg config.Config, o *orchestrator.Orchestrator) (svc *core.Service, q *eventqueue.Queue, mp metric.MeterProvider, storeClose func() error, err error) {
	store, err := eventqueue.NewFileStore(filepath.Join(cfg.LogDir, "queue.jsonl"))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open event queue: %w", err)
	}
	mp = cfg.Meter()
	emitter, err := metrics.New(mp, func() map[string]int { return q.DepthByType() })
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("construct metrics emitter: %w", err)
	}
	q, err = eventqueue.New(store, eventqueue.WithRetryBackoff(cfg.RetryBackoff), eventqueue.WithObserver(emitter), eventqueue.WithSerializeTypes(cfg.SerializeTypes...))
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("construct event queue: %w", err)
	}
	for _, r := range cfg.Roles {
		if !r.Enabled {
			// Declared but inactive this run: its bindings still count for
			// Bindings.Declares below (INV-DISP-3's configuration-wide view), but
			// no Listener is registered, so its events wait, are offered to
			// nobody, and expire unconsumed (INV-EVT-1, INV-EVT-4).
			slog.Info("role disabled; not registering a listener", "role", r.Name)
			continue
		}
		q.Register(o.NewListener(ctx, r))
	}
	svc, err = core.Listen(core.Options{
		LogDir:   cfg.LogDir,
		Queue:    q,
		Bindings: core.NewBindings(declaredBindTypes(cfg.Roles)...),
		Observer: emitter,
	})
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("start core: %w", err)
	}
	return svc, q, mp, store.Close, nil
}

// declaredBindTypes collects every event type SOME configured role binds,
// INCLUDING a role disabled for this run: core.Bindings must answer "does the
// CONFIGURATION declare this type" (INV-DISP-3 / INV-WORKFLOW-1), never "is
// some active listener bound to it" — that second, narrower question is what
// bootCore's per-role Listener registration (skipping disabled roles) answers
// instead.
func declaredBindTypes(rs roles.RoleSet) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs {
		for _, b := range r.Binds {
			if !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	return out
}

// preparedRun is the config/precheck/eventlog setup shared by `run` and
// `run-until-idle` — the same setup runDrain used to do before this bead
// retired it.
type preparedRun struct {
	cfg     config.Config
	o       *orchestrator.Orchestrator
	cleanup func() // closes the eventlog writer, if one was opened
}

// prepareRun loads config, warns on stale env / tracked config / stub queries /
// stranded feedback, prechecks bd reachability, applies this run's --only/
// --disable selectors (STORY-OP-3), resolves self_login, and wires the
// orchestrator + its event log — everything `run` / `run-until-idle` need
// before they may touch the queue or the core. On failure it prints the same
// diagnostic runDrain used to and returns a non-OK exit code; the caller MUST
// check code before using the returned preparedRun.
//
// sel's selectors are applied AFTER precheck (which calls cfg.Validate())
// deliberately, never before: applySelectors' own doc comment (selectors.go)
// explains why a re-Validate against the run-scoped subset would produce
// false findings — precheck must see the FULL, unfiltered cfg, and nothing
// past this point may call Validate() again.
func prepareRun(ctx context.Context, sel runSelectors) (preparedRun, int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return preparedRun{}, exitPrecheck
	}
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	warnDroppedRoleEnv()
	warnTrackedConfig(ctx, cfg)
	warnStubQueries(cfg)

	slog.Info("starting", "repo", cfg.RepoRoot, "config", cfg.ConfigPath, "roles", len(cfg.Roles))
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return preparedRun{}, exitPrecheck
	}
	slog.Info("precheck ok", "prefix", cfg.BeadsPrefix)

	cfg, err = applySelectors(cfg, sel)
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

	if cfg.SelfLogin == "" {
		selfLogin, err := resolveSelf(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve self:", err)
			return preparedRun{}, exitPrecheck
		}
		cfg.SelfLogin = selfLogin
	}
	slog.Info("resolved self", "login", cfg.SelfLogin)
	warnStrandedFeedback(ctx, br, cfg.SelfLogin)

	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
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
	return preparedRun{cfg: cfg, o: o, cleanup: cleanup}, exitOK
}

// runRunUntilIdle implements `pr-pool run-until-idle` (and the deprecated
// `drain` alias): boot the core, fire ONE producer tick (matching the single
// discovery pass a `drain` invocation used to run), drain the durable queue
// until it is idle (INV-LIFE-1's drain-and-exit mode: every enqueued event
// accepted or expired, no offer outstanding), then close the core and tear
// down every pr-pool-* session. It never touches internal/eventbus or a
// per-role Cap — both retired by this bead (pg2-f3mcb.2).
//
// only/disable are this invocation's --only/--disable flag occurrences
// (STORY-OP-3, DEC-CLI-1), as parsed by parseRunLikeArgs; resolveSelectors
// folds in PR_POOL_ONLY/PR_POOL_DISABLE before prepareRun applies them.
func runRunUntilIdle(only, disable []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pr, code := prepareRun(ctx, resolveSelectors(only, disable))
	if code != exitOK {
		return code
	}
	defer pr.cleanup()

	if pr.o.Gated() {
		slog.Info("gated; pausing without dispatch")
		return exitOK // NOTE: gated exit boots no core and tears nothing down (nothing was created)
	}

	svc, q, mp, storeClose, err := bootCore(ctx, pr.cfg, pr.o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-until-idle:", err)
		return exitGeneric
	}
	defer func() { _ = storeClose() }()
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()

	defer pr.o.TeardownAll(context.Background())
	defer func() {
		_ = svc.Close()
		if err := <-accepted; err != nil {
			slog.Warn("core accept loop exited with an error", "err", err)
		}
	}()

	if err := pr.o.ProduceTick(ctx, q); err != nil {
		fmt.Fprintln(os.Stderr, "run-until-idle: discover:", err)
		return exitGeneric
	}
	if err := q.RunUntilIdle(ctx, idleDrainTick); err != nil {
		fmt.Fprintln(os.Stderr, "run-until-idle:", err)
		return exitGeneric
	}
	// Force a final metrics snapshot before exit: without this, a short run that
	// starts and finishes between two periodic collections of a REAL backend
	// would report nothing (the no-op default has nothing to flush regardless).
	if err := metrics.Flush(ctx, mp); err != nil {
		slog.Warn("run-until-idle: metrics flush failed", "err", err)
	}
	slog.Info("run-until-idle: queue drained")
	return exitOK
}

// runRun implements `pr-pool run`: boot the core and run indefinitely,
// producing and dispatching on cfg.PollInterval, until SIGINT/SIGTERM requests
// an orderly shutdown (INV-LIFE-1's daemon mode). It stays reachable to push
// participants throughout — the socket Accept loop runs the whole time,
// sharing the SAME *eventqueue.Queue the produce/dispatch loop drives, so a
// push arriving mid-tick is picked up on the next dispatch.
//
// only/disable are this invocation's --only/--disable flag occurrences
// (STORY-OP-3, DEC-CLI-1); see runRunUntilIdle's doc comment.
func runRun(only, disable []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pr, code := prepareRun(ctx, resolveSelectors(only, disable))
	if code != exitOK {
		return code
	}
	defer pr.cleanup()

	svc, q, _, storeClose, err := bootCore(ctx, pr.cfg, pr.o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return exitGeneric
	}
	defer func() { _ = storeClose() }()
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()

	defer pr.o.TeardownAll(context.Background())
	defer func() {
		_ = svc.Close()
		if err := <-accepted; err != nil {
			slog.Warn("core accept loop exited with an error", "err", err)
		}
	}()

	tick := pr.cfg.PollInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		if pr.o.Gated() {
			slog.Info("gated; pausing without dispatch")
		} else if err := pr.o.ProduceTick(ctx, q); err != nil {
			slog.Error("producer tick failed", "err", err)
		} else {
			q.Dispatch()
			q.Expire()
		}
		select {
		case <-ctx.Done():
			slog.Info("run: shutdown requested")
			return exitOK
		case <-ticker.C:
		}
	}
}
