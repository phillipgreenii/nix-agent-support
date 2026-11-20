package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/x/jsonllogger"
	"github.com/phillipgreenii/x/lifecycle"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/daemon"
	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"

	"github.com/phillipgreenii/pg-router/internal/telemetry"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
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

// The shutdown phases (drain, preShutdown hooks, in-flight tail, flush) have no
// constants of their own: shutdown_budget.go splits ONE deadline between them,
// and that deadline is the --shutdown-deadline flag the deployment passes (the
// nix module derives launchd's ExitTimeOut from the same number). The phases
// the daemon runs once its signal context ends are, in order:
//
//   - drain: in-flight dispatches of roles that do not survive shutdown finish,
//     then their context is cancelled (a cut offer is re-offered by the next
//     daemon). A role with survivesShutdown is neither waited on nor cancelled
//     (ADR 0085).
//   - metrics_flush, accept_close: final metrics snapshot, socket closed.
//   - pre_shutdown: the handlers' preShutdown hooks, bounded.
//   - store_close: cancelled dispatches unwind (the tail), then the store closes.
//
// A phase runs under min(its share, what is left of the deadline), so an
// earlier overrun shrinks later phases instead of pushing past the watchdog.

// inFlightDrainPollInterval is WaitForInFlightDrain's poll cadence during
// the tail wait.
const inFlightDrainPollInterval = 100 * time.Millisecond

// drainThenCloseStore waits (bounded by timeout, the tail share of the deadline) for every
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
func drainThenCloseStore(ctx context.Context, q *eventqueue.Queue, storeClose func() error, timeout time.Duration, survives func(listenerID string) bool) {
	// ctx is typically already cancelled (the signal that began shutdown); the drain
	// must outlive that cancellation, so it keeps only ctx's values.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if !q.WaitForInFlightDrainExcept(drainCtx, inFlightDrainPollInterval, survives) {
		slog.Error("shutdown: in-flight dispatch offer(s) did not drain before the timeout; closing the durable store anyway -- any still-outstanding offer's durable write will be lost",
			"timeout", timeout, "sessionsInFlight", q.SessionsInFlight())
	}
	_ = storeClose()
}

// drainThenCancelDispatch is runRun's graceful-shutdown step: it waits (up to
// timeout) for every Kick()-launched offer still in flight on q to finish --
// dispatches run under a context decoupled from the signal context, so they are
// NOT cancelled by SIGTERM -- and then cancels the dispatch context, which
// unsticks any handler that outlived the timeout (wireclient's
// exec.CommandContext kills it). The caller MUST run this before
// preShutdownAll so a handler's shutdown hook never races a live dispatch.
//
// survives (nil: none) names the listeners whose dispatches survive a
// shutdown (bead pg2-dtigc, ADR 0085): their listener contexts are not derived
// from the dispatch context, so cancelDispatch does not reach them. They are
// excluded from the wait -- a ccpool-backed dispatch lasts far longer than any
// drain budget launchd allows -- and left running, supervised by their own
// handler, until the next daemon re-offers the event and absorbs the session.
func drainThenCancelDispatch(ctx context.Context, q *eventqueue.Queue, cancelDispatch context.CancelFunc, timeout time.Duration, survives func(listenerID string) bool) {
	// ctx is typically already cancelled (the signal that began shutdown); the drain
	// must outlive that cancellation, so it keeps only ctx's values.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if !q.WaitForInFlightDrainExcept(drainCtx, inFlightDrainPollInterval, survives) {
		slog.Info("shutdown: in-flight dispatch(es) did not finish before the drain timeout; cancelling them (cut offers are re-offered by the next daemon)",
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
//
// Routing on the new handler path (docket pg2-12gr4.10): UNCHANGED, every
// enabled role gets its own postStartup call whichever path it is on. A role
// with handler reaches its own adapter (with the role object on stdin), so
// every adapter named by an enabled role is covered, and the old shared handler
// receives it per old-path role exactly as before. Unlike preShutdown it is not
// deduplicated: the ccpool adapter's post-startup reconcile is per role.
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

// preShutdownAll dispatches handler.preShutdown ONCE per distinct handler per
// daemon shutdown — each adapter a role names with handler, through the first
// enabled role naming it — at the same point TeardownAll used to fire
// (pg2-oju6w.15). With one adapter this is exactly the pg2-asr8z behavior
// below: one call, to the first enabled role's handler.
//
// SUPERSEDES pg2-oju6w.15's original "dispatch once per enabled role"
// shape (bead pg2-asr8z, 2026-09-22): that shape called this hook once per
// enabled role, on the theory (recorded in
// the ccpool adapter's own preshutdown.go runPreShutdown doc, "decision #1") that N redundant sweeps were merely
// harmless waste. Production evidence (pg2-1kq1x's verification, root-caused
// as pg2-asr8z) proved that framing wrong: with N enabled roles, this used
// to launch N sequential fresh "preShutdown" SUBPROCESSES, and every one of
// them ran its own full ccpool-list-and-close sweep (measured ~0.74s per
// `ccpool list --all` alone) — sequentially eating enough of launchd's 5s
// ExitTimeOut that the daemon was SIGKILLed before even the FIRST sweep
// finished, let alone the rest. Calling it once is safe because that sweep
// is GLOBAL and invariant across roles: it keys only on the handler
// process's own launch config (PG_ROUTER_CCPOOL_HANDLER_CONFIG, inherited
// unchanged by every subprocess spawn) and never reads the per-role config
// at all — so every enabled role's call of one adapter would have
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
	// Routing (docket pg2-12gr4.10, the handler split): every distinct handler
	// named by an enabled role receives preShutdown exactly ONCE, in config
	// order, carried by the first enabled role that names it. An adapter's
	// shutdown sweep is global to the adapter (ccpool lists and closes every
	// session it owns), so a second call for another role it serves would
	// repeat the identical, slow sweep that pg2-asr8z measured eating launchd's
	// ExitTimeOut; a different adapter binary still gets its own. No adapter
	// named by an enabled role is left without its call.
	seen := map[string]bool{}
	for _, r := range cfg.Roles {
		if !r.Enabled || seen[r.Handler] {
			continue
		}
		seen[r.Handler] = true
		if _, err := o.Handler.PreShutdown(ctx, r); err != nil {
			slog.Warn("preShutdown failed", "role", r.Name, "err", err)
		}
	}
}

// preparedRun is the config/precheck/eventlog setup shared by `run` and
// `run-until-idle` — the same setup runDrain used to do before this bead
// retired it.
type preparedRun struct {
	cfg     config.Config
	o       *orchestrator.Orchestrator
	cleanup func() // closes the eventlog writer, if one was opened
	// Shutdown seams (the lifecycle group and its budget); zero values select the
	// production defaults: stderr logger, defaultShutdownDeadline, os.Exit.
	shutdownLog      *slog.Logger
	shutdownDeadline time.Duration
	shutdownExit     func(code int)
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
// the ccpool adapter's own startup pre-flight
// (cmd/pg-router-handler-ccpool/preflight.go) — pg-router's own pre-runtime
// validation is exactly INV-WORKFLOW-1's six determinable conditions,
// unrelated to bd/self_login.
//
// sel's selectors are applied AFTER config.Load() (whose own preamble already
// calls cfg.Validate() — internal/config/config.go) deliberately, never
// before: applySelectors' own doc comment (selectors.go) explains why a
// re-Validate against the run-scoped subset would produce false findings —
// nothing past this point may call Validate() again.
// slowStartupStep is the duration past which a prepareRun step is logged.
const slowStartupStep = 1 * time.Second

// startupStepTimer returns a func that, called at the end of each startup
// step with its name, logs the step's name and duration when it took longer
// than threshold, so a startup stall names the blocking call.
func startupStepTimer(l *slog.Logger, threshold time.Duration) func(name string) {
	last := time.Now()
	return func(name string) {
		now := time.Now()
		if d := now.Sub(last); d > threshold {
			l.Warn("slow startup step", "step", name, "duration", d)
		}
		last = now
	}
}

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
	// goroutine dump). The retired PR tool's own internal/sync/daemon.go sidesteps this
	// the same way (NewTextHandler/NewJSONHandler build a handler directly,
	// never touch slog.Default()) — mirrored here rather than rediscovered.
	slog.SetDefault(slog.New(jsonllogger.Fanout(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
		telemetry.NewSlogHandler(),
	)))

	step := startupStepTimer(slog.Default(), slowStartupStep)
	cfg, err := config.LoadContext(ctx)
	step("load config")
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return preparedRun{}, exitPrecheck
	}
	warnDroppedRoleEnv()
	step("warn dropped role env")
	warnTrackedConfig(ctx, cfg)
	step("warn tracked config")
	warnStubQueries(cfg)
	step("warn stub queries")

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
func runRunUntilIdle(only, disable []string, shutdownDeadline time.Duration) int {
	g := lifecycle.New(context.Background(), runGroupOptions(shutdownDeadline, nil, nil))
	pr, code := prepareRun(g.Context(), resolveSelectors(only, disable))
	if code != exitOK {
		_ = g.Run()
		return code
	}
	defer pr.cleanup()
	pr.shutdownDeadline = shutdownDeadline
	return runInGroup(g, "run-until-idle", func(ctx context.Context) int { return runUntilIdleBody(ctx, pr) })
}

// runInGroup runs body as the one loop of g and returns its exit code once the
// group has finished. The group owns the signal context (a second signal kills
// the process) and the shutdown watchdog: a body still running when the
// deadline passes after the first signal is cut by a logged hard exit.
func runInGroup(g *lifecycle.Group, name string, body func(ctx context.Context) int) int {
	var code int
	g.Go(name, func(ctx context.Context) error {
		code = body(ctx)
		return nil
	})
	_ = g.Run()
	return code
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
	// Same shutdown rule as runLongRunningBody: no chained re-offers after a stop
	// request (bead pg2-ag31a).
	stopChaining := context.AfterFunc(ctx, q.StopChaining)
	defer stopChaining()
	budget := newShutdownBudget(pr.shutdownDeadline)
	clk := newShutdownClock(ctx, budget.Deadline)
	validateRolesAtStartup(ctx, pr.o, pr.cfg)
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
	defer func() { drainThenCloseStore(ctx, q, storeClose, clk.Within(budget.Tail), nil) }()
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()

	// Shutdown work runs after ctx (the signal) is cancelled, so it must not inherit the cancellation.
	defer func() {
		preCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clk.Within(budget.PreShutdown))
		defer cancel()
		preShutdownAll(preCtx, pr.o, pr.cfg)
	}()
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
		if err := metrics.Flush(context.WithoutCancel(ctx), mp); err != nil {
			slog.Warn("run-until-idle: metrics flush failed", "err", err)
		}
	}()

	// INV-LIFE-2's paused drain-and-exit slice: the router pause is a level in the
	// event log, so it is only knowable once the core is booted. With the router
	// paused this run still boots (above) and stays reachable — a concurrent
	// `ingest-event` push still succeeds and is durably enqueued — but it never
	// produces or drains: a pause stops admission for every registration, so a
	// drain-to-idle could spin on events a blocked listener will not take until
	// the router is resumed. The reachability
	// window is bounded to ONE idleDrainTick pass; expiry still runs once
	// (INV-EVT-4's clock does not pause), and the final metrics snapshot is
	// still flushed by the defers above. It MUST NOT report the queue as
	// drained — it never drained anything.
	if snap := q.Levels(); pauseNotice(snap) != "" {
		fmt.Fprintln(os.Stderr, pauseNotice(snap))
		q.Expire()
		select {
		case <-ctx.Done():
		case <-time.After(idleDrainTick):
		}
		slog.Info("run-until-idle: paused; skipped drain (no dispatch), expiry advanced")
		return exitOK
	}

	// Level sources answer first (bounded wait), so admission sees levels.
	// A pull still running when this run ends is cancelled with it.
	pullCtx, cancelPulls := context.WithCancel(ctx)
	defer cancelPulls()
	if p := pr.o.LevelPuller(); len(p.Sources()) > 0 {
		p.StartPass(pullCtx, pr.cfg.Levels.StartWait)
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
		sourceReports := sourceReportsFor(pr.cfg.Queries, rpt, pr.o.LastTick())
		attachCalendar(sourceReports, pr.cfg.Queries, "", now)
		svc.PublishTick(core.TickSnapshot{
			Sources:    sourceReports,
			Config:     resolvedConfigFor(pr.cfg, core.RunModeDrainAndExit),
			RunMode:    core.RunModeDrainAndExit,
			Version:    version,
			LastTickAt: now,
			SnapshotAt: now,
		})
		// A registration a level requirement blocks is not waited on: work only it
		// could take does not hold the run open (it is reported below).
		if q.IdleExceptPaused() {
			break
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "run-until-idle:", ctx.Err())
			return exitGeneric
		case <-time.After(idleDrainTick):
		}
	}
	reportPausedByLevels(os.Stderr, q.PausedRoles())
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

// reportPausedByLevels names, one line per registration, what a level
// requirement still blocks when run-until-idle exits: "run-until-idle: worker is
// paused by levels (disk >= OK: disk is TIGHT)". A blocked registration neither
// delays the exit nor changes its status.
func reportPausedByLevels(w io.Writer, paused []eventqueue.PausedRole) {
	for _, p := range paused {
		parts := make([]string, 0, len(p.Result.Failing))
		for _, f := range p.Result.Failing {
			parts = append(parts, fmt.Sprintf("%s: %s is %s", f.ClauseText, f.Resource, f.Level))
		}
		fmt.Fprintf(w, "run-until-idle: %s is paused by levels (%s)\n", textsafe.Sanitize(p.Role), textsafe.Sanitize(strings.Join(parts, "; ")))
	}
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
func runRun(only, disable []string, metricsAddr string, shutdownDeadline time.Duration) int {
	g := lifecycle.New(context.Background(), runGroupOptions(shutdownDeadline, nil, nil))
	pr, code := prepareRun(g.Context(), resolveSelectors(only, disable))
	if code != exitOK {
		_ = g.Run()
		return code
	}
	defer pr.cleanup()
	pr.shutdownDeadline = shutdownDeadline

	// Opt-in pprof endpoint (PG_ROUTER_PPROF_ADDR): `run` only, never
	// run-until-idle or another subcommand. Reports go to stderr.
	defer startPprofFromEnv(os.Getenv, os.Stderr)()

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

	return runInGroup(g, "daemon", func(ctx context.Context) int { return runDaemon(ctx, g, pr, 0) })
}

// runLongRunningBody is runRun's boot-to-exit body over an already prepared
// run, split out (like runUntilIdleBody) so a test can drive it with a
// hand-built preparedRun and an injectable drainTimeout. ctx is the SIGNAL
// context: it stops the tick loop and the core accept loop. Dispatches run
// under a separate dispatchCtx that is cancelled only after the shutdown drain
// (drainThenCancelDispatch), so a SIGTERM lets in-flight handlers finish
// instead of SIGKILLing them (bead pg2-euh4f).
func runLongRunningBody(ctx context.Context, pr preparedRun, drainTimeout time.Duration) int {
	// The group owns the signal context and the shutdown watchdog: once ctx ends
	// the shutdown has pr.shutdownDeadline to finish or the watchdog logs
	// shutdown_overrun naming the phase in flight and exits 10. A caller's ctx
	// ending stands in for the first signal.
	g := lifecycle.New(ctx, runGroupOptions(pr.shutdownDeadline, pr.shutdownLog, pr.shutdownExit))
	return runInGroup(g, "daemon", func(ctx context.Context) int { return runDaemon(ctx, g, pr, drainTimeout) })
}

// runDaemon is the daemon loop: boot, tick, and on ctx ending (the first
// signal) drain and tear down in the phases shutdown_budget.go budgets. g
// receives the phase names so an overrun report says where shutdown hung. A
// zero drainTimeout selects the budget's drain share.
func runDaemon(ctx context.Context, g *lifecycle.Group, pr preparedRun, drainTimeout time.Duration) int {
	budget := newShutdownBudget(pr.shutdownDeadline)
	clk := newShutdownClock(ctx, budget.Deadline)
	if drainTimeout <= 0 {
		drainTimeout = budget.Drain
	}
	// Dispatches run decoupled from the signal context so SIGTERM does not cancel them.
	dispatchCtx, cancelDispatch := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelDispatch()
	svc, q, mp, storeClose, err := bootCore(dispatchCtx, pr.cfg, pr.o, pr.declaredRoles, pr.excluded, core.RunModeLongRunning)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return exitGeneric
	}
	// No new offers once shutdown is requested, including the re-offer a settling
	// Kick offer makes for its listener's next head.
	stopChaining := context.AfterFunc(ctx, q.StopChaining)
	defer stopChaining()
	validateRolesAtStartup(ctx, pr.o, pr.cfg)
	// Non-blocking, fire-and-forget per role, no dispatch gate (see
	// startPostStartupAsync); run-until-idle keeps the blocking postStartupAll.
	startPostStartupAsync(ctx, pr.o, pr.cfg)
	// survives names the roles whose dispatches this shutdown leaves running
	// (ADR 0085); the drain and the store close do not wait on them.
	survivors := survivingRoles(pr.cfg)
	survives := func(listenerID string) bool { return survivors[listenerID] }
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()
	// Registered here so it is observed by the final metrics snapshot: the
	// shutdown duration and overrun signals land in it.
	signals := registerCommonSignals(mp)

	tick := pr.cfg.PollInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	// The explicit shutdown order reproduces the effective order of the former
	// LIFO defers: duration signals, final metrics snapshot, accept loop close,
	// handlers' preShutdown hooks, then the store close. The final snapshot
	// runs before the store-close drain, so the very last accept settling
	// during that drain can be invisible to it (an accepted, pre-existing
	// final-snapshot limitation; reordering would make the flush block on the
	// drain, a behavior change).
	app := daemon.NewApp(daemon.AppConfig{
		Interval: tick,
		// The first dispatch pass of this process runs BEFORE its first producer
		// tick (DEC-EVENT-5): a fresh process has no last-fire time, so its first
		// tick polls every source one after another, and what the durable queue
		// restored is already queued. The initial level pull pass runs before it,
		// with a bounded wait (ADM-6). Silent once shutdown was requested.
		Startup: func(ctx context.Context) {
			startLevelPulls(ctx, pr.cfg, pr.o.LevelPuller())
			if ctx.Err() == nil {
				slog.Info("run: startup dispatch pass, before the first producer tick", "offersLaunched", q.Kick())
			}
		},
		Tick: func(ctx context.Context, prevGates string) string {
			return runOneTick(ctx, pr.cfg, pr.o, svc, q, prevGates, os.Stderr)
		},
		Drain: func(ctx context.Context) {
			drainThenCancelDispatch(ctx, q, cancelDispatch, clk.Within(drainTimeout), survives)
		},
		SetPhase: g.SetPhase,
		Shutdown: []daemon.Step{
			{Run: func() { observeShutdown(context.WithoutCancel(ctx), signals, clk) }},
			{Phase: "metrics_flush", Run: func() {
				if err := metrics.Flush(context.WithoutCancel(ctx), mp); err != nil {
					slog.Warn("run: metrics flush failed", "err", err)
				}
			}},
			{Phase: "accept_close", Run: func() {
				_ = svc.Close()
				if err := <-accepted; err != nil {
					slog.Warn("core accept loop exited with an error", "err", err)
				}
			}},
			{Phase: "pre_shutdown", Run: func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clk.Within(budget.PreShutdown))
				defer cancel()
				preShutdownAll(ctx, pr.o, pr.cfg)
			}},
			{Phase: "store_close", Run: func() {
				drainThenCloseStore(ctx, q, storeClose, clk.Within(budget.Tail), survives)
			}},
		},
	})
	app.Run(ctx)
	return exitOK
}
