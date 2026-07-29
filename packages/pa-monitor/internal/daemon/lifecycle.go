package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/gofrs/flock"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/timing"
)

// PIDLock holds the pidfile flock for the lifetime of the daemon process.
// Release MUST be called to remove the file and free the lock. Safe to
// call multiple times.
type PIDLock struct {
	file     *flock.Flock
	path     string
	released bool
}

// AcquirePIDFile creates Paths.Dir if missing, opens the pidfile, takes
// a non-blocking exclusive flock, and writes the current pid into the
// file.
//
// If a previous daemon died without releasing the lock, the kernel has
// already freed it — TryLock will succeed and we overwrite the stale
// pid content. No explicit stale-detection is needed for that case.
//
// Returns an error if the lock is held by a LIVE process.
func AcquirePIDFile(p Paths) (*PIDLock, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}

	fl := flock.New(p.PIDFile)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("flock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("pidfile %s is locked by another process", p.PIDFile)
	}

	pid := []byte(strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(p.PIDFile, pid, 0o600); err != nil {
		_ = fl.Unlock()
		return nil, fmt.Errorf("write pid: %w", err)
	}

	return &PIDLock{file: fl, path: p.PIDFile}, nil
}

// Release frees the lock and removes the pidfile. Safe to call multiple
// times; subsequent calls are no-ops.
func (l *PIDLock) Release() {
	if l == nil || l.released {
		return
	}
	l.released = true
	_ = l.file.Unlock()
	_ = os.Remove(l.path)
}

// BindSocket removes any pre-existing socket file at p.Socket, binds a
// fresh Unix listener, and chmods it 0600. The returned listener removes
// the socket file on Close.
func BindSocket(p Paths) (net.Listener, error) {
	if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	l, err := net.Listen("unix", p.Socket)
	if err != nil {
		return nil, fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return &socketListener{Listener: l, path: p.Socket}, nil
}

// socketListener wraps net.Listener so that Close unlinks the socket
// file in addition to closing the underlying fd.
type socketListener struct {
	net.Listener
	path string
}

func (s *socketListener) Close() error {
	err := s.Listener.Close()
	_ = os.Remove(s.path)
	return err
}

// RunOptions configures a daemon run. Paths is required; everything else
// is optional. Emitter, when non-nil, is shut down on Run return so any
// batched metrics/logs flush before the process exits.
//
// PollerInterface is the contract the daemon needs from the poller. The
// concrete *poller.Poller satisfies it; tests inject smaller fakes.
type PollerInterface interface {
	Snapshot(ctx context.Context) (*aggregate.Tree, bool, error)
}

// BlockWeekIDSetter is an optional extension of PollerInterface. When the
// PollerInterface value also implements this interface, RunWith assigns the
// surrogate block/week ids after each cost upsert so that the next
// Snapshot call can attach contributions to the correct parent rows.
type BlockWeekIDSetter interface {
	SetActiveBlockID(id int64)
	SetActiveWeekID(id int64)
}

// SessionLabeler is an optional extension of PollerInterface. When the
// PollerInterface value also implements it, RunWith injects a label function
// wired to the daemon's label pipeline (detectors + decorators + cardinality
// cap + per-session cache) so Snapshot PERSISTS each session's computed labels
// (workspace.scope etc.) to the DB — the same pipeline that feeds OTEL, so the
// stored labels and the emitted labels agree (pg2-4xbrm).
type SessionLabeler interface {
	SetLabeler(fn func(sv *aggregate.SessionView) map[string]string)
}

// When Poller is non-nil, each tick calls Snapshot, folds the result
// into the shared state visible to gRPC handlers, and feeds the block
// and week trackers (if provided).
type RunOptions struct {
	Paths        Paths
	Emitter      *otel.Emitter
	Tick         time.Duration
	Poller       PollerInterface
	BlockTracker *block.Tracker
	WeekTracker  *week.Tracker
	// Caffeinate, when non-nil, has its Tick advanced each main tick
	// with the current any-working signal derived from the snapshot.
	Caffeinate *caffeinate.Manager
	// InitialCaffeinateOn applies the persisted user toggle at startup.
	InitialCaffeinateOn bool
	// InitialAutoResumeEnabled applies the persisted auto-resume toggle at
	// startup. Read from the ToggleStore (DB) — the single source of truth
	// since the runtime.json -> SQLite migration. RunWith seeds the live
	// WatermarkStore from it so the toggle survives daemon restarts.
	InitialAutoResumeEnabled bool
	// RuntimePath is the runtime.json file path. Empty disables persistence
	// of caffeinate toggle changes from Caffeinate RPC.
	RuntimePath string
	// Account carries the plan identity and pricing inputs (the per-block /
	// per-week caps) used by the store-conversion path. Its zero value yields
	// zero caps ("unknown"), matching the pre-Account default for an unset tier.
	Account account.Account
	// PlanTier is forwarded as the `plan_tier` attribute on emitted
	// limit-hit events/counters.
	PlanTier string
	// WeeklyEvery controls how often the weekly cost projection is read from the
	// Monitor + upserted, relative to the main tick (0 = once per tick). It bounds
	// the weekly DB-write + histogram cadence (pre-fold this gated the ccusage
	// weekly walk; the value now comes from the Monitor's UsagePricing observer).
	WeeklyEvery int
	// Version is the build identifier reported on DaemonState. Defaults to "dev".
	Version string
	// Nudger config — passed to nudger.TickContext each tick. Defaults
	// applied if zero. NudgerSignalers must be non-empty for the daemon
	// to construct its WatermarkStore and accept NudgeQueue / NudgeCancel
	// / SetAutoResume RPCs.
	AutoResumeMessage string
	AutoResumeDelay   time.Duration
	DisruptGrace      time.Duration
	EscalationAfter   time.Duration
	NudgerSignalers   []signal.Signaler
	// Detectors run against each session at tick time to derive labels
	// for emitted metrics. Built-in detectors live in
	// internal/labels/detectors. Empty → only the {state, plan_tier}
	// label set is emitted on sessions.count.
	Detectors []labels.Detector
	// Decorators are shell-out label producers. Run alongside Detectors;
	// their output wins on conflicting keys.
	Decorators []*labels.Decorator
	// ConfigPath, when non-empty together with ReloadDecorators, enables the
	// per-tick config-reload watch (bead pg2-r1f1j.8): the daemon re-reads this
	// file each tick and rebuilds the decorator pipeline when it changes, so a
	// decorator added by `pn workspace apply` is picked up without a manual
	// daemon restart (the launchd agent's restart is keyed on the package hash,
	// not the config, and the daemon can boot before the config is written).
	ConfigPath string
	// ReloadDecorators re-loads the config at ConfigPath and returns the fresh
	// decorator pipeline (already adapted via labels.AsFailable). Nil disables
	// the reload watch (the startup Decorators are used for the daemon's life).
	ReloadDecorators func() ([]labels.FailableDetector, error)
	// LabelCap caps the number of distinct values for any one label key.
	// Past the cap, additional values bucket as "other". 0 → 50.
	LabelCap int
	// TreeObserver, if non-nil, is called with the fully-annotated tree
	// just before it is published to gRPC clients. Used in tests to
	// inspect the tree (including PendingNudge annotations) without going
	// through the gRPC layer.
	TreeObserver func(*aggregate.Tree)
	// BridgeRegistry, if non-nil, is wired into the gRPC server so
	// RegisterBridge handlers can record cmux-bridge presence + last-seen.
	// The same registry should be passed to the poller (PollerInterface
	// implementations that consult it) so terminal-host refinement works.
	BridgeRegistry *bridge.Registry
	// CmuxAncestor, if non-nil alongside BridgeRegistry, is the function the
	// RegisterBridge handler uses to walk a bridge PID's ancestry to its
	// cmux server PID. Typically (*signal.CmuxSignaler).FindCmuxServerAncestor.
	CmuxAncestor func(pid int) (int, bool)
	// WriteService, when non-nil, receives block and week upserts after each
	// cost tick. The returned surrogate ids are assigned to the poller's
	// ActiveBlockID / ActiveWeekID so the next contribution-upsert pass has them.
	WriteService *service.WriteService
	// ReadService, when non-nil, is wired into sharedState so snapshot()
	// materialises the aggregate.Tree from the DB on each call.
	ReadService *service.ReadService
	// DB, when non-nil alongside WriteService, is used by the nudge recorder
	// adapter to resolve session string ids to surrogate row ids before
	// persisting nudge events.
	DB *sql.DB
	// SessionsDir is the directory where Claude Code writes .json session files.
	// When non-empty alongside WriteService, an hourly GC sweeper goroutine is
	// started to reconcile files with the DB and hard-delete stale rows.
	SessionsDir string
	// BridgeSnapshotInterval and BridgeStaleAfter are the timing-derived
	// connection windows (see internal/timing): the per-bridge snapshot push
	// cadence and the registry stale cutoff. Zero selects timing defaults, so a
	// caller that does not set them still gets a consistent pair.
	BridgeSnapshotInterval time.Duration
	BridgeStaleAfter       time.Duration
}

// RunWith is the daemon's main loop. It acquires the pidfile, binds the
// socket, starts the gRPC server, and blocks until ctx is done.
func RunWith(ctx context.Context, opts RunOptions) error {
	lock, err := AcquirePIDFile(opts.Paths)
	if err != nil {
		return err
	}
	defer lock.Release()

	lis, err := BindSocket(opts.Paths)
	if err != nil {
		return err
	}
	defer func() { _ = lis.Close() }()

	state := newSharedState()
	state.mu.Lock()
	state.runtimePath = opts.RuntimePath
	state.autoResumeDelay = opts.AutoResumeDelay
	state.mu.Unlock()
	state.setCaffeinateOn(opts.InitialCaffeinateOn)
	if opts.Caffeinate != nil {
		opts.Caffeinate.SetToggle(opts.InitialCaffeinateOn)
	}
	if opts.ReadService != nil {
		state.setReadService(opts.ReadService)
	}
	// Wire the live cwd -> *PRInfo source so snapshot() re-annotates the
	// DB-materialised tree with PR info (F1). PRInfo is not persisted, so the
	// served tree would otherwise drop every PR. The poller satisfies
	// prByDirQuerier via MonitorPRByDir (an atomic read of the producer's
	// published DerivedState); a bare test fake that doesn't implement it simply
	// leaves the served tree PR-less, as before.
	if q, ok := opts.Poller.(prByDirQuerier); ok {
		state.setPRByDirSource(q)
	}

	// bridgeReg is the single bridge.Registry instance shared by the gRPC
	// server (BridgeChannel attaches live bridge streams to it), the
	// delivery path's bridgeDeliverer (looks up a live stream to push a
	// Deliver), and the reaper (prunes members whose process has died).
	// Defaulting to a fresh registry when BridgeRegistry is unset keeps
	// bridgeDeliverer's reg field always non-nil; the bridge route is simply
	// unreachable in that configuration since cmuxAncestorFn below then also
	// has nothing to resolve against.
	// Connection-timing windows. Both derive from the same base cadences (see
	// internal/timing), so the registry stale cutoff and the per-bridge snapshot
	// push interval cannot drift into an inversion. Zero opts values fall back to
	// the timing defaults as a consistent pair.
	timingDefaults := timing.Derive(timing.Config{})
	snapshotInterval := opts.BridgeSnapshotInterval
	if snapshotInterval <= 0 {
		snapshotInterval = timingDefaults.SnapshotInterval
	}
	staleAfter := opts.BridgeStaleAfter
	if staleAfter <= 0 {
		staleAfter = timingDefaults.StaleAfter
	}

	bridgeReg := opts.BridgeRegistry
	if bridgeReg == nil {
		bridgeReg = bridge.NewRegistry(staleAfter)
	}
	// cmuxAncestorFn resolves a target PID's cmux server ancestor for
	// delivery routing. serve()'s bridges param tolerates nil, so
	// opts.CmuxAncestor may legitimately be nil too; guard with an
	// always-false stand-in so compositeDeliverer routes every target
	// in-daemon instead of nil-derefing on opts.CmuxAncestor(pid).
	cmuxAncestorFn := opts.CmuxAncestor
	if cmuxAncestorFn == nil {
		cmuxAncestorFn = func(int) (int, bool) { return 0, false }
	}
	// tr correlates in-flight bridge deliveries to their DeliverResult acks
	// (see delivery.go). It must exist before serve() (which wires
	// tr.resolve/tr.failServer into the BridgeChannel handler's inbound
	// hooks) and before bridgeDel (which blocks on it per delivery).
	tr := newTracker()
	bridgeDel := &bridgeDeliverer{reg: bridgeReg, ancestor: cmuxAncestorFn, tr: tr, timeout: 5 * time.Second}

	version := opts.Version
	if version == "" {
		version = "dev"
	}
	_, stop := serve(lis, state, version, opts.PlanTier, opts.AutoResumeMessage, opts.WriteService, bridgeReg, snapshotInterval, tr.resolve, tr.failServer, opts.Emitter.MeterProvider())
	defer stop()

	// Reap bridge members whose process has died, against the same registry
	// instance the gRPC server and delivery path use.
	go RunReaper(ctx, bridgeReg, 30*time.Second, pidAlive)

	defer func() { _ = opts.Emitter.Shutdown(context.Background()) }()

	// One-shot migration: copy runtime.json toggles into the DB, then delete
	// the file. Runs before NewWatermarkStore so that, on first startup after
	// migration, the WatermarkStore sees an absent file (empty state) rather
	// than the stale JSON. Best-effort — log the error but continue startup.
	if opts.RuntimePath != "" && opts.WriteService != nil {
		if err := MigrateRuntimeJSON(
			ctx, opts.RuntimePath,
			opts.WriteService.Toggles(),
			opts.WriteService.Nudges(),
			opts.WriteService.Sessions(),
		); err != nil {
			// Non-fatal: daemon can still run without the migration.
			_ = fmt.Errorf("runtime.json migration (non-fatal): %w", err)
		}
	}

	// Launch the hourly GC sweeper when a WriteService and a sessions directory
	// are both available.
	if opts.WriteService != nil && opts.SessionsDir != "" {
		sweeper := &GCSweeper{
			SessionsDir:     opts.SessionsDir,
			WriteService:    opts.WriteService,
			Interval:        time.Hour,
			HardDeleteAfter: 24 * time.Hour,
		}
		go sweeper.Run(ctx)
	}

	// Construct Nudger + WatermarkStore when configured.
	if opts.RuntimePath != "" && len(opts.NudgerSignalers) > 0 {
		watermarks, err := NewWatermarkStore(opts.RuntimePath, opts.Emitter)
		if err != nil {
			return fmt.Errorf("read runtime.json: %w", err)
		}
		// Seed the live auto-resume toggle from the persisted value (DB →
		// InitialAutoResumeEnabled). Previously this was loaded only from
		// runtime.json, which the SQLite migration deletes — so the toggle
		// silently reset to false on every daemon restart.
		watermarks.SetAutoResumeEnabled(opts.InitialAutoResumeEnabled)
		// inDaemonDel wraps the existing synchronous in-daemon signal layer
		// (tmux/ghostty/vscode) unchanged. cmux-hosted targets are routed to
		// bridgeDel (built above, sharing bridgeReg/cmuxAncestorFn/tr with
		// serve() and the reaper) instead.
		//
		// ADR 0022 (daemon MUST NOT execute cmux): the delivery slice is
		// stripped of any CmuxSignaler via WithoutCmux, so the SignalerAdapter
		// literally cannot resolve a cmux signaler for delivery. This is
		// structural — it does not depend on CmuxSignaler.Detect returning
		// false for in-daemon targets in the shipped config (emergent coupling
		// that a nil-CmuxAncestor or split-instance wiring could reopen). The
		// D5 keep-awake predicate below deliberately keeps the FULL
		// opts.NudgerSignalers (it only Detects, never Sends) so a cmux-hosted
		// disrupt still holds the Mac awake.
		deliverySignalers := signal.WithoutCmux(opts.NudgerSignalers)
		inDaemonDel := &inDaemonDeliverer{sig: &SignalerAdapter{Signalers: deliverySignalers}}
		deliverer := &compositeDeliverer{ancestor: cmuxAncestorFn, bridge: bridgeDel, inDaemon: inDaemonDel}
		var nr nudger.NudgeRecorder
		if opts.WriteService != nil && opts.DB != nil {
			nr = &nudgeRecorder{ws: opts.WriteService, db: opts.DB}
		}
		// Surface nudge_history write failures to stderr (captured by launchd →
		// launchd-stderr.log). This sink is deliberately export-INDEPENDENT: the
		// OTel counter/log path can be silently failing to export, and the DB row
		// is the fallback capture — so when the row write itself fails the error
		// must not be discarded (it previously was, leaving failed deliveries with
		// no trace in any sink).
		historyErrLog := func(msg string) { fmt.Fprintf(os.Stderr, "nudger: %s\n", msg) }
		n := nudger.New(deliverer, watermarks, nr, historyErrLog)
		n.LoadStore(watermarks.LoadIntents())
		state.mu.Lock()
		state.nudger = n
		state.watermarks = watermarks
		state.mu.Unlock()
		state.setPendingNudgeQueue(n)
	}

	// The account-level limit-hit is fired from the AUTHORITATIVE signal in the
	// tick loop below (ADR 0024 D3), NOT from the trackers' retired cost-cap
	// trigger. The trackers are retained only for the block.id / week.id labels.

	tick := opts.Tick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	// Baseline liveness (bead pg2-r1f1j.6). The OTel log stream is otherwise
	// silent between discrete events, so a quiet daemon looks dead on the Loki
	// panel. A one-shot daemon.started event opens the {service_name="pa-monitor"}
	// stream the moment the run loop begins, and a modest daemon.heartbeat below
	// keeps it alive on every healthy tick window (NOT every tick — heartbeatEvery
	// throttles the ~5s tick down to heartbeatInterval).
	heartbeatEvery := heartbeatEveryN(tick, heartbeatInterval)
	opts.Emitter.LogEvent("daemon.started", map[string]string{
		"version":   version,
		"plan_tier": opts.PlanTier,
	})

	capLimit := opts.LabelCap
	if capLimit <= 0 {
		capLimit = 50
	}
	labelCap := labels.NewCardinalityCap(capLimit)
	labelCache := map[string]labels.Set{}
	// Adapt the concrete decorators to the FailableDetector interface once, so
	// the label path can skip caching a decorator that FAILED this tick (vs a
	// successful-empty result). Cached failures would otherwise freeze a wrong
	// (empty) label set for the session's lifetime (ADR 0024 D5).
	decorators := labels.AsFailable(opts.Decorators)

	// Wire the poller's persistence labeler to the same pipeline that feeds
	// OTEL so Snapshot stores workspace.scope etc. on the session row instead
	// of nil (pg2-4xbrm). The closure captures `decorators`/`labelCache` by
	// reference: both are reassigned/cleared on config reload below, and the
	// closure runs on this (the tick) goroutine via Snapshot — race-free, same
	// as updateGauges/updateSessionInfo. Snapshot runs each tick BEFORE those,
	// so it warms the shared cache the OTEL calls then reuse.
	if sl, ok := opts.Poller.(SessionLabeler); ok {
		sl.SetLabeler(func(sv *aggregate.SessionView) map[string]string {
			return labelsForSession(sv, opts.Detectors, decorators, labelCap, labelCache)
		})
	}

	// Wire the poller's phase/scan/subprocess recorder to the emitter (pg2-sewtz
	// OTel instrumentation). Deliberately done through an anonymous interface
	// (not poller.PhaseRecorder) so this package does not import internal/core/poller
	// — Go interface satisfaction for the *poller.Poller assertion would force
	// that import. Guarded by opts.Emitter != nil per the typed-nil-interface
	// rule: a nil *otel.Emitter boxed into PhaseRecorder would be a non-nil
	// interface wrapping a nil pointer, which the poller's `p.Rec != nil` checks
	// would then treat as present.
	if opts.Emitter != nil {
		if s, ok := opts.Poller.(interface{ SetPhaseRecorder(any) }); ok {
			s.SetPhaseRecorder(opts.Emitter)
		}
	}

	// configReloader (bead pg2-r1f1j.8): when enabled, re-reads the config file
	// each tick and swaps in a freshly-rebuilt decorator pipeline whenever the
	// file changes — so a decorator written by `pn workspace apply` after the
	// daemon booted is picked up without a manual restart. Its fingerprint
	// starts empty, so the first tick always rebuilds, deterministically closing
	// the boot race regardless of what config the daemon read at startup.
	var reloader *configReloader
	if opts.ReloadDecorators != nil && opts.ConfigPath != "" {
		reloader = &configReloader{path: opts.ConfigPath, rebuild: opts.ReloadDecorators}
	}

	// previousErrors tracks the last-seen LastError.At per session so we
	// can detect newly advanced errors and fire the api_error.observed counter.
	previousErrors := map[string]time.Time{}

	// Account-level limit-hit latches (ADR 0024 D3/R7). Each fires at most once
	// per authoritative window; a changed reset time re-arms. State persists
	// across ticks (declared outside the loop) like previousErrors above.
	var blockLimitHit, weekLimitHit limitHitLatch

	// phase records a once-per-tick lifecycle phase duration. opts.Emitter's
	// RecordPhase is nil-safe, so this is always callable regardless of
	// whether an Emitter was configured.
	phase := func(name string, start time.Time) { opts.Emitter.RecordPhase(name, time.Since(start)) }

	// Phase 3: decouple corpus/provider work from the emit tick. When the poller
	// exposes the producer lifecycle, start its single producer goroutine (it owns
	// Monitor+provider assembly and publishes the DerivedState the tick Loads) and
	// stop+join it on return. Test fakes that don't implement this stay synchronous.
	if pr, ok := opts.Poller.(interface {
		StartProducer(context.Context)
		StopProducer()
	}); ok {
		pr.StartProducer(ctx)
		defer pr.StopProducer()
	}

	tickCount := 0
	// runTick is the whole per-tick body, extracted into a closure (rather than
	// left inline in the `case <-t.C:` arm) so RecordTickDuration can be recorded
	// via a single defer that covers EVERY exit path — including the early
	// no-poller and snapshot-error returns below, which previously `continue`d
	// the select loop directly. Converting those two `continue`s to `return`
	// preserves identical control flow (both only ever skipped the rest of one
	// tick), now inside the closure. All other `continue` statements deeper in
	// this function belong to their own nested `for` loops and are unaffected.
	runTick := func() {
		tickStart := time.Now()
		defer func() { opts.Emitter.RecordTickDuration(time.Since(tickStart)) }()

		tickCount++
		// Pick up a changed decorator config without a restart (pg2-r1f1j.8).
		// Runs in this (the tick) goroutine, so swapping `decorators` and
		// clearing `labelCache` is race-free — both are owned here.
		if reloader != nil {
			if newDecs, ok := reloader.reloadIfChanged(); ok {
				decorators = newDecs
				clear(labelCache)
				fmt.Fprintf(os.Stderr, "daemon: decorator config reloaded (%d decorator(s))\n", len(newDecs))
			}
		}
		if opts.Poller == nil {
			// no poller — still advance the (toggle-driven) caffeinate
			// tick to honour RPC-driven on/off requests
			if opts.Caffeinate != nil {
				// re-read user toggle from shared state in case Caffeinate RPC changed it
				opts.Caffeinate.SetToggle(state.isCaffeinateOn())
				opts.Caffeinate.Tick(false)
			}
			return
		}
		tree, _, err := opts.Poller.Snapshot(ctx)
		if err != nil {
			return
		}
		// Read the account-global rate_limits (ADR 0021 §1 / ADR 0029) + the
		// current-week cost from the corpus Monitor's projections. The Monitor's
		// single per-tick walk already produced them (pg2-5sxkb); the pre-fold
		// opts.Limits / opts.WeeklyFn walks were removed in pg2-66h9g. A nil reading
		// leaves the tree's values untouched — never clobbered with 0.
		tickNow := time.Now().UTC()

		// rate_limits + weekly are read from the poller's PUBLISHED DerivedState
		// (producer-owned). The producer fires the "limits"/"weekly" phase timers
		// off-tick (step 6); the tick only reads the projection and applies it.
		if lr := monitorLimits(opts.Poller); lr != nil {
			applyLimits(tree, lr)
		}

		// Preserve the WeeklyEvery cadence: read + upsert the weekly cost only on
		// ~1/WeeklyEvery ticks (the pre-fold walk cadence), so the weekly histogram
		// sample rate and the UpsertWeek DB-write rate are unchanged even though the
		// value now comes from the (cheap) published DerivedState projection.
		fetchWeek := opts.WeeklyEvery <= 0 || tickCount%opts.WeeklyEvery == 0
		if fetchWeek {
			if w := monitorWeekly(opts.Poller, tickNow); w != nil {
				tree.ActiveWeek = w
			}
		}

		// Persist the active block and week to the DB, then propagate
		// their surrogate ids to the poller so contribution upserts in the
		// next Snapshot have valid parent ids.
		dbWriteBlockStart := time.Now()
		if opts.WriteService != nil {
			if tree.ActiveBlock != nil {
				nowUTC := time.Now().UTC()
				// Persist the block WITH the current rate_limits reading so the
				// store->tree (GetState) path reflects it (ADR 0021 §6).
				b := blockToStoreBlockWithLimits(tree.ActiveBlock, opts.Account.BlockCap(), nowUTC, tree)
				if blockID, err := opts.WriteService.UpsertBlock(ctx, b); err == nil {
					if setter, ok := opts.Poller.(BlockWeekIDSetter); ok {
						setter.SetActiveBlockID(blockID)
					}
				}
			}
			if tree.ActiveWeek != nil {
				nowUTC := time.Now().UTC()
				w := weekToStoreWeek(tree.ActiveWeek, opts.Account.WeekCap(), nowUTC)
				if weekID, err := opts.WriteService.UpsertWeek(ctx, w); err == nil {
					if setter, ok := opts.Poller.(BlockWeekIDSetter); ok {
						setter.SetActiveWeekID(weekID)
					}
				}
			}
		}
		phase("db_write_block", dbWriteBlockStart)

		if opts.BlockTracker != nil {
			opts.BlockTracker.Update(tree.ActiveBlock)
		}
		if opts.WeekTracker != nil {
			opts.WeekTracker.Update(tree.ActiveWeek)
		}
		// ADR 0024 D2: keep awake when any session is Working OR Blocked on a
		// machine-recoverable blocker (usage_limit — auto-resume fires at
		// reset — or a retryable error). session.KeepAwake collapses the
		// per-session predicate; iterate sessions since the Directory counts
		// don't carry the blocker/retryable breakdown needed here.
		anyWorking := false
		for _, sv := range tree.Sessions() {
			if sv.Session == nil {
				continue
			}
			if session.KeepAwake(sv.Status, sv.Blocker, sv.LastErrorRetryable) {
				anyWorking = true
				break
			}
		}
		// D5: a terminal nudgeable error with zero recorded nudge attempts
		// keeps the Mac awake until the first attempt. Computed INLINE here
		// from tree + watermark store (NOT the nudger's pending-store —
		// that reconciles later in this same tick, at n.Reconcile below, so
		// its grace/pending state is empty during the 0–30s disrupt grace).
		// Reading LastError directly makes the predicate true at T+0, before
		// idle-sleep could fire during the grace.
		state.mu.RLock()
		wmCaffeinate := state.watermarks
		state.mu.RUnlock()
		anyUnattemptedNudgeableDisrupt := false
		if wmCaffeinate != nil && wmCaffeinate.AutoResumeEnabled() {
			// Uses the FULL opts.NudgerSignalers (NOT the cmux-stripped
			// delivery slice): D5 only calls ResolveSignaler→Detect, never
			// Send, so keeping CmuxSignaler here lets a cmux-hosted disrupt
			// hold the Mac awake without the daemon ever exec'ing cmux
			// (ADR 0022). Do not swap this for the delivery slice.
			anyUnattemptedNudgeableDisrupt = hasUnattemptedNudgeableDisrupt(tree, wmCaffeinate, opts.NudgerSignalers)
		}
		keepAwake := anyWorking || anyUnattemptedNudgeableDisrupt
		if opts.Caffeinate != nil {
			toggleOn := state.isCaffeinateOn()
			opts.Caffeinate.SetToggle(toggleOn)
			prevState := opts.Caffeinate.State()
			opts.Caffeinate.Tick(keepAwake)
			newState := opts.Caffeinate.State()
			// active is true when the subprocess is running OR when the
			// user toggle is on but the manager is waiting for agents to
			// start before spawning (StateOff + toggle=true). Without the
			// toggleOn guard, a tick with keepAwake=false would reset
			// caffeinateActive to false immediately after the user flips
			// the toggle on, causing the TUI indicator to revert.
			active := newState != caffeinate.StateOff || toggleOn
			cause := ""
			if active {
				if toggleOn {
					cause = "manual"
				}
				if keepAwake {
					cause = "agents_active"
				}
			}
			// Store BOTH indicators: the legacy collapsed `active` flag plus
			// the richer PROCESS state (newState) and its grace countdown.
			// The MODE (toggleOn) is already on caffeinateOn. Reading the
			// manager's real state here revives the long-dead grace display.
			graceRemaining := opts.Caffeinate.GraceRemaining()
			state.setCaffeinateState(active, cause, newState, graceRemaining)
			opts.Emitter.RecordCaffeinateActive(active, caffeinateProcessLabel(newState), int(graceRemaining.Seconds()), map[string]string{"plan_tier": opts.PlanTier})
			if prevState == caffeinate.StateOff && newState != caffeinate.StateOff {
				opts.Emitter.RecordCaffeinateRound(map[string]string{"cause": cause})
			}
			if prevState == caffeinate.StateArmedCountdown && newState == caffeinate.StateOff && !keepAwake {
				opts.Emitter.RecordCaffeinateGraceExpired(nil)
			}
		}
		// Push the auto-resume intent each tick so its observable gauge
		// tracks the daemon's actual setting (mirrors caffeinate). Reads
		// from the WatermarkStore which is the source of truth for the
		// nudger's auto-resume flag.
		state.mu.RLock()
		wmForGauge := state.watermarks
		state.mu.RUnlock()
		if wmForGauge != nil {
			opts.Emitter.RecordAutoResumeEnabled(wmForGauge.AutoResumeEnabled(), map[string]string{
				"plan_tier": opts.PlanTier,
			})
		}
		// Deferral visibility (ADR 0024 D5): publish how many sessions have
		// auto-resume deliberately WAITING on a window (window_pending) as a
		// GAUGE, so the operator can distinguish "auto-resume is waiting" from
		// "broken". Carry-forward-zero in the emitter drops the cause to 0 when
		// the window clears (mirrors sessions.count).
		autoResumeOn := wmForGauge != nil && wmForGauge.AutoResumeEnabled()
		opts.Emitter.RecordNudgeDeferred(deferredNudgeCounts(tree, autoResumeOn, time.Now()))
		if tree.ActiveBlock != nil {
			opts.Emitter.RecordBlockCost(tree.ActiveBlock.CostUSD, map[string]string{
				"plan_tier": opts.PlanTier,
				"block.id":  tree.ActiveBlock.ID,
			})
		}
		if tree.ActiveWeek != nil {
			weekID := ""
			if opts.WeekTracker != nil {
				weekID = opts.WeekTracker.ID()
			}
			opts.Emitter.RecordWeekCost(tree.ActiveWeek.TotalCost, map[string]string{
				"plan_tier": opts.PlanTier,
				"week.id":   weekID,
			})
		}
		// Authoritative status-line rate_limits usage gauges (ADR 0021 §5).
		// ACCOUNT-GLOBAL: only plan_tier — no session_id, no block/week id. The
		// *.cost.usd gauges above keep emitting native cost, unchanged; these are
		// the honest percentage/reset signals. Unknown windows (nil pct / zero
		// reset) are not observed at all (handled inside the emitter).
		acctAttrs := map[string]string{"plan_tier": opts.PlanTier}
		opts.Emitter.RecordBlockUsage(tree.FiveHourPct, tree.FiveHourResetsAt, acctAttrs)
		opts.Emitter.RecordWeekUsage(tree.SevenDayPct, tree.SevenDayResetsAt, acctAttrs)

		// Account-level limit-hit from the AUTHORITATIVE signal (ADR 0024 D3),
		// latched once-per-window (R7). FiveHourPct is populated by applyLimits
		// above; the trigger also fires on any terminal per-session usage_limit.
		// The retired ccusage CostUSD>=capUSD trigger no longer fires this.
		if blockLimitHit.observe(blockLimitTrigger(tree), tree.FiveHourResetsAt) {
			blockID := ""
			if opts.BlockTracker != nil {
				blockID = opts.BlockTracker.ID()
			}
			opts.Emitter.RecordBlockLimitHit(map[string]string{
				"plan_tier": opts.PlanTier,
				"block.id":  blockID,
			})
		}
		// Weekly counterpart (R11): SevenDayPct is nil-guarded inside
		// weekLimitTrigger, so a nil reading never fires and never panics.
		if weekLimitHit.observe(weekLimitTrigger(tree), tree.SevenDayResetsAt) {
			weekID := ""
			if opts.WeekTracker != nil {
				weekID = opts.WeekTracker.ID()
			}
			opts.Emitter.RecordWeekLimitHit(map[string]string{
				"plan_tier": opts.PlanTier,
				"week.id":   weekID,
				"source":    "computed",
			})
		}
		updateGauges(opts.Emitter, tree, opts.PlanTier, opts.Detectors, decorators, labelCap, labelCache)
		updateSessionInfo(opts.Emitter, tree, opts.PlanTier, opts.Detectors, decorators, labelCap, labelCache)
		// Drop stale label cache entries for sessions that vanished.
		pruneLabelCache(labelCache, tree)

		// Emit api_error.observed for each newly-seen error, and
		// snapshot sessions.errored gauge per kind.
		emitErrorMetrics(opts.Emitter, tree, previousErrors)

		// Baseline liveness heartbeat (bead pg2-r1f1j.6): a modest
		// daemon.heartbeat every heartbeatEvery ticks (~heartbeatInterval)
		// guarantees the OTel log stream stays alive even when no discrete
		// event fires, so a healthy-but-quiet daemon isn't NO DATA on Loki.
		// autoResumeOn is computed earlier in this tick from the WatermarkStore.
		if shouldHeartbeat(tickCount, heartbeatEvery) {
			opts.Emitter.LogEvent("daemon.heartbeat", heartbeatAttrs(tree, opts.PlanTier, autoResumeOn))
		}

		// Run nudger tick after tree is built and before publishing to clients.
		state.mu.RLock()
		n := state.nudger
		wm := state.watermarks
		state.mu.RUnlock()
		nudgeStart := time.Now()
		if n != nil {
			msg := opts.AutoResumeMessage
			if msg == "" {
				msg = "continue"
			}
			tctx := nudger.TickContext{
				Now:               time.Now(),
				Tree:              tree,
				AutoResumeEnabled: wm.AutoResumeEnabled(),
				AutoResumeMessage: msg,
				AutoResumeDelay:   opts.AutoResumeDelay,
				DisruptGrace:      opts.DisruptGrace,
				EscalationAfter:   opts.EscalationAfter,
				Watermarks:        wm,
				// Producer-side no-surface gate (bead pg2-gjekd): reap
				// surfaceless "ghost" sessions from the candidate set so they
				// are never enqueued. Uses the FULL opts.NudgerSignalers
				// (Detect-only, never Send) — the same predicate the D5
				// keep-awake disjunct uses (hasUnattemptedNudgeableDisrupt),
				// so a cmux-hosted target resolves without the daemon exec'ing
				// cmux (ADR 0022). Deeper fix complementing pg2-2o0p7's
				// dispatcher-side suppress-and-drop backstop.
				HasSurface: func(pid int) bool {
					return signal.ResolveSignaler(opts.NudgerSignalers, pid) != nil
				},
			}
			n.Reconcile(tctx)
			// Annotate sessions BEFORE dispatch so clients see what's queued.
			// Pending-nudge sources surface as PendingNudge.Sources; nudge
			// history (LastNudgedAt + LastNudgeSources) is sourced from the
			// watermark store and surfaces for every session that has ever
			// received a nudge — independent of whether anything is currently
			// pending.
			for _, dir := range tree.Dirs {
				for _, sv := range dir.Sessions {
					sid := sv.SessionID
					if n.PendingFor(sid) {
						sources := n.SourcesFor(sid)
						strs := make([]string, 0, len(sources))
						for _, s := range sources {
							strs = append(strs, string(s))
						}
						sv.PendingNudge = &aggregate.PendingNudge{Sources: strs}
					}
					wmSession := wm.SessionWatermark(sid)
					if !wmSession.LastNudgedAt.IsZero() {
						sv.LastNudgedAt = wmSession.LastNudgedAt
						sv.LastNudgeSources = wmSession.LastNudgeSources
					}
				}
			}
			n.Dispatch(ctx, tctx)
			wm.SaveIntents(n.SnapshotStore())

			// Escalation flip: surface LastErrorRetryable=false on terminal
			// errors for sessions whose watermark marks DisruptEscalated.
			// Also persist the flip to the DB so it survives a restart.
			for _, dir := range tree.Dirs {
				for _, sv := range dir.Sessions {
					if sv.LastError == nil || !sv.LastError.IsTerminal {
						continue
					}
					swm := wm.SessionWatermark(sv.SessionID)
					if !swm.DisruptEscalated {
						continue
					}
					// The retryable verdict now lives on the view, not the
					// shared record — flip it in place (no record copy needed).
					sv.LastErrorRetryable = false
					// Persist the flip so the DB-materialised path sees it.
					if opts.WriteService != nil {
						_ = opts.WriteService.MarkSessionEscalated(ctx, sv.SessionID)
					}
				}
			}
		}
		phase("nudge", nudgeStart)

		if opts.TreeObserver != nil {
			opts.TreeObserver(tree)
		}

		// Refresh the cached snapshot on THIS (tick) goroutine so gRPC
		// handlers (buildState) serve it without a synchronous SQLite read on
		// their own goroutine — keeping the BridgeChannel writer's snapshot
		// cadence independent of DB latency. See sharedState.refreshSnapshot.
		state.refreshSnapshot()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			runTick()
		}
	}
}

// hasUnattemptedNudgeableDisrupt reports whether any session in the tree has a
// terminal, nudgeable error with ZERO recorded nudge attempts — the D5 error
// keep-awake disjunct. It mirrors the disrupt producer's nudge gates (terminal
// + Retryable + !FromSubagent + a resolvable signaler) but is computed
// independently of the nudger so it is true at T+0 (before the disrupt grace
// elapses and anything is enqueued). The caller has already verified
// AutoResumeEnabled. A session counts as "zero recorded attempts" when its
// LastDisruptAttemptAt watermark is zero OR predates this error's timestamp.
func hasUnattemptedNudgeableDisrupt(tree *aggregate.Tree, wm *WatermarkStore, signalers []signal.Signaler) bool {
	if tree == nil || wm == nil {
		return false
	}
	for _, dir := range tree.Dirs {
		for _, sv := range dir.Sessions {
			// A session blocked on a human (permission prompt / AskUserQuestion /
			// re-auth) suppresses BOTH nudges and caffeinate (§6/D3; ADR 0024 R4:
			// WaitingForHuman → blocker ∈ human_*). Even if it carries a
			// terminal-retryable LastError, no nudge is ever attempted, so it must
			// not hold the Mac awake via the D5 error keep-awake disjunct.
			if sv.Status == session.Blocked && sv.Blocker.IsHuman() {
				continue
			}
			le := sv.LastError
			if le == nil || !le.IsTerminal {
				continue
			}
			if !transcript.Retryable(le) {
				continue
			}
			if le.FromSubagent {
				continue
			}
			if signal.ResolveSignaler(signalers, sv.PID) == nil {
				continue
			}
			attempt := wm.SessionWatermark(sv.SessionID).LastDisruptAttemptAt
			if attempt.IsZero() || attempt.Before(le.At) {
				return true
			}
		}
	}
	return false
}

// updateGauges pushes session counts grouped by (state + per-workspace +
// per-agent labels) into the emitter gauges. Production always passes a
// non-empty detector set (see cmd/pa-monitor/daemon.go), so this function
// assumes detector-based emission. nil-safe on emitter.
func updateGauges(
	e *otel.Emitter,
	tree *aggregate.Tree,
	planTier string,
	detectors []labels.Detector,
	decorators []labels.FailableDetector,
	cap *labels.CardinalityCap,
	labelCache map[string]labels.Set,
) {
	if e == nil || tree == nil {
		return
	}

	type groupKey string
	counts := map[groupKey]int{}
	groupLabels := map[groupKey]labels.Set{}

	for _, sv := range tree.Sessions() {
		ls := labelsForSession(sv, detectors, decorators, cap, labelCache)
		// labelsForSession returns a fresh copy — safe to mutate.
		ls["state"] = session.Status(sv.Status).String()
		// ADR 0024: attach the blocker as its own attribute when Blocked so the
		// dashboard can break "blocked" down by reason. Absent otherwise.
		if sv.Session != nil && sv.Blocker != session.NoBlocker {
			ls["blocker"] = sv.Blocker.String()
		}
		key := groupKey(canonicalKey(ls))
		counts[key]++
		if _, ok := groupLabels[key]; !ok {
			groupLabels[key] = ls
		}
	}

	groups := make([]otel.SessionGroup, 0, len(counts))
	for k, c := range counts {
		groups = append(groups, otel.SessionGroup{
			Count:  c,
			Labels: groupLabels[k],
		})
	}
	e.RecordSessionGroups(groups, map[string]string{"plan_tier": planTier})
}

// updateSessionInfo builds one OTel row per non-Dormant session and pushes
// it through e.RecordSessionInfo. "Non-Dormant" mirrors the TUI's active
// view; dormant sessions live indefinitely in the tree so emitting them
// would unbound session_id cardinality on the exporter. nil-safe on emitter.
//
// Each row carries the columns the dashboard's active-sessions table needs:
// session_id, session_name, cwd, terminal_host (abbreviated), status,
// model, error_kind (empty when no terminal error), tokens, cost. The
// session's labels.Set is also forwarded so workspace.scope / agent.* etc.
// can filter the table via Grafana variables.
func updateSessionInfo(
	e *otel.Emitter,
	tree *aggregate.Tree,
	planTier string,
	detectors []labels.Detector,
	decorators []labels.FailableDetector,
	cap *labels.CardinalityCap,
	labelCache map[string]labels.Set,
) {
	if e == nil {
		return
	}
	e.RecordSessionInfo(buildSessionInfoRows(tree, planTier, detectors, decorators, cap, labelCache))
}

// buildSessionInfoRows is the pure row-builder split out of updateSessionInfo
// so tests can assert the filter + column derivation without touching the
// OTel SDK. tree==nil returns nil. Dormant sessions are skipped — the
// cardinality cap policy that mirrors the TUI's active view.
func buildSessionInfoRows(
	tree *aggregate.Tree,
	planTier string,
	detectors []labels.Detector,
	decorators []labels.FailableDetector,
	cap *labels.CardinalityCap,
	labelCache map[string]labels.Set,
) []otel.SessionInfo {
	if tree == nil {
		return nil
	}
	rows := make([]otel.SessionInfo, 0)
	for _, sv := range tree.Sessions() {
		if sv == nil || sv.Session == nil {
			continue
		}
		// Skip long-idle (formerly Dormant) sessions to bound session_id
		// cardinality on the exporter (ADR 0024 R10: the idle-age exclusion
		// survives the Dormant→idle rename). Uses the poller-set LongIdle flag
		// (which also applies the live-PID clamp) on this live-tree path.
		if sv.LongIdle {
			continue
		}
		errKind := ""
		if sv.LastError != nil && sv.LastError.IsTerminal {
			errKind = string(sv.LastError.Kind)
		}
		blocker := ""
		if sv.Blocker != session.NoBlocker {
			blocker = sv.Blocker.String()
		}
		ls := labelsForSession(sv, detectors, decorators, cap, labelCache)
		// Pass plan_tier through as a baseline label so the dashboard's
		// $plan_tier filter applies to this gauge too.
		ls["plan_tier"] = planTier
		rows = append(rows, otel.SessionInfo{
			SessionID:    sv.SessionID,
			SessionName:  sv.Name,
			Cwd:          sv.Cwd,
			TerminalHost: session.TerminalAbbrev(sv.TerminalHost),
			Status:       session.Status(sv.Status).String(),
			Blocker:      blocker,
			Model:        sv.Model,
			ErrorKind:    errKind,
			Tokens:       int64(sv.SessionTokens),
			CostUSD:      sv.CostUSD,
			Labels:       ls,
		})
	}
	return rows
}

// labelsForSession runs detectors + decorators against the session,
// caches the result by session.id (labels are static for the session's
// lifetime), and applies the cardinality cap to every value.
//
// The returned Set is a fresh copy of the cache entry. Callers may mutate
// it freely (e.g. to attach transient labels like `state` per tick)
// without polluting future cache hits.
//
// Decorator failures are NOT cached (ADR 0024 D5). A decorator that fails
// transiently (timeout / non-zero exit / parse error) reports ok=false via
// DetectOK; on such a tick the merged result is used for THIS tick's emission
// but is deliberately left out of the cache, so the next tick retries the
// decorator instead of freezing the (wrong) empty result for the session's
// whole lifetime. A successful-empty decorator result still caches, and
// detectors (which never fail) are always cached.
func labelsForSession(
	sv *aggregate.SessionView,
	detectors []labels.Detector,
	decorators []labels.FailableDetector,
	cap *labels.CardinalityCap,
	cache map[string]labels.Set,
) labels.Set {
	if sv == nil || sv.Session == nil {
		return labels.Set{}
	}
	cached, ok := cache[sv.SessionID]
	if !ok {
		s := labels.Session{
			ID:    sv.SessionID,
			PID:   sv.PID,
			CWD:   sv.Cwd,
			Env:   sv.Env,
			Model: sv.Model,
		}
		merged := labels.Set{}
		for _, d := range detectors {
			merged = merged.Merge(d.Detect(s))
		}
		decoratorFailed := false
		for _, dec := range decorators {
			out, decOK := dec.DetectOK(s)
			if !decOK {
				decoratorFailed = true
			}
			merged = merged.Merge(out)
		}
		if cap != nil {
			for k, v := range merged {
				merged[k] = cap.Cap(k, v)
			}
		}
		// Only cache a clean tick. On a decorator failure we still return the
		// merged set for this tick but skip the cache so the next tick retries.
		if !decoratorFailed {
			cache[sv.SessionID] = merged
		}
		cached = merged
	}
	// Defensive copy: callers must be free to mutate without affecting
	// the cache. Cheap — label sets are small.
	out := make(labels.Set, len(cached))
	for k, v := range cached {
		out[k] = v
	}
	return out
}

// pruneLabelCache drops cache entries for sessions no longer in the
// tree, freeing memory across long-running daemons.
func pruneLabelCache(cache map[string]labels.Set, tree *aggregate.Tree) {
	if cache == nil || tree == nil {
		return
	}
	live := map[string]struct{}{}
	for _, sv := range tree.Sessions() {
		if sv != nil && sv.Session != nil {
			live[sv.SessionID] = struct{}{}
		}
	}
	for id := range cache {
		if _, ok := live[id]; !ok {
			delete(cache, id)
		}
	}
}

// canonicalKey returns a stable string representing the label set so we
// can group identical sets in a map.
func canonicalKey(ls labels.Set) string {
	keys := make([]string, 0, len(ls))
	for k := range ls {
		if ls[k] == "" {
			continue
		}
		keys = append(keys, k)
	}
	// sort for stability
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	var b []byte
	for i, k := range keys {
		if i > 0 {
			b = append(b, '|')
		}
		b = append(b, k...)
		b = append(b, '=')
		b = append(b, ls[k]...)
	}
	return string(b)
}

// emitErrorMetrics fires api_error.observed counters for newly-advanced
// session errors and updates the sessions.errored observable gauge. It
// mutates previousErrors in place.
func emitErrorMetrics(e *otel.Emitter, tree *aggregate.Tree, previousErrors map[string]time.Time) {
	if tree == nil {
		return
	}
	for _, dir := range tree.Dirs {
		for _, sv := range dir.Sessions {
			le := sv.LastError
			if le == nil {
				continue
			}
			kind := string(le.Kind)
			if le.At.After(previousErrors[sv.SessionID]) {
				previousErrors[sv.SessionID] = le.At
				isTerminalStr := "false"
				if le.IsTerminal {
					isTerminalStr = "true"
				}
				e.RecordApiErrorObserved(map[string]string{
					"session_id":  sv.SessionID,
					"kind":        kind,
					"is_terminal": isTerminalStr,
				})
				// A context-window-exceeded error ("prompt is too long") is a
				// distinct, separately-counted condition. Fire it on the same
				// newly-advanced edge so each hit is counted once.
				if le.IsContextLimit {
					e.RecordContextLimitHit(map[string]string{
						"session_id": sv.SessionID,
						"model":      sv.Model,
					})
				}
			}
		}
	}
	e.RecordSessionsErrored(buildErroredCounts(tree))
}

// buildErroredCounts counts, keyed by (kind, is_terminal), the sessions that
// are CURRENTLY erroring — i.e. presently blocked on an error or usage-limit
// (ADR 0024 status model). A session that recovered (working/idle) still
// carries a stale LastError, but counting those made "Sessions errored" read
// non-zero when nothing was actually erroring (bead pg2-...). Keyed by
// is_terminal so the dashboard can filter to terminal errors (ADR 0024 D5).
func buildErroredCounts(tree *aggregate.Tree) map[otel.ErroredKey]int {
	counts := map[otel.ErroredKey]int{}
	if tree == nil {
		return counts
	}
	for _, dir := range tree.Dirs {
		for _, sv := range dir.Sessions {
			if sv == nil || sv.Session == nil {
				continue
			}
			le := sv.LastError
			if le == nil {
				continue
			}
			if sv.Status != session.Blocked ||
				(sv.Blocker != session.ErrorBlocker && sv.Blocker != session.UsageLimit) {
				continue
			}
			counts[otel.ErroredKey{Kind: string(le.Kind), IsTerminal: le.IsTerminal}]++
		}
	}
	return counts
}

// deferredNudgeCounts derives, keyed by cause, the count of sessions where
// auto-resume is deliberately WAITING on a window rather than broken (ADR 0024
// D5). During a hit window the window_reset producer defers silently until the
// reset — previously emitting ZERO telemetry, so an operator could not tell
// "auto-resume is waiting" from "broken". This feeds the pa_monitor.nudge.deferred
// GAUGE (a snapshot of how many are currently deferred — the producers re-decide
// every tick, so a counter would inflate massively).
//
// cause="window_pending": auto-resume enabled AND a session is Blocked on
// usage_limit with a still-in-future per-session reset (RateLimitResetsAt) —
// exactly the condition under which the window_reset producer waits rather than
// nudges. A reset-less usage-limit pause (handled by the limit_pause producer)
// has no future reset and is intentionally NOT counted here (it retries once per
// window rather than waiting silently). With auto-resume off the producers cancel
// rather than defer, so nothing is "waiting"; the empty map lets the emitter's
// carry-forward-zero drop the gauge to 0.
func deferredNudgeCounts(tree *aggregate.Tree, autoResumeEnabled bool, now time.Time) map[string]int {
	counts := map[string]int{}
	if tree == nil || !autoResumeEnabled {
		return counts
	}
	for _, sv := range tree.Sessions() {
		if sv == nil || sv.Session == nil {
			continue
		}
		if sv.Status != session.Blocked || sv.Blocker != session.UsageLimit {
			continue
		}
		if sv.RateLimitResetsAt.IsZero() || !sv.RateLimitResetsAt.After(now) {
			continue
		}
		counts["window_pending"]++
	}
	return counts
}

// heartbeatInterval is the modest baseline cadence at which RunWith emits a
// daemon.heartbeat log event. Chosen well above the ~5s tick so a healthy
// daemon produces a steady but low-volume liveness stream — the tick itself is
// far too chatty for a log record every time.
const heartbeatInterval = 60 * time.Second

// heartbeatEveryN converts the desired heartbeat interval into a tick count:
// emit every Nth tick where N = round(interval/tick). Clamped to a minimum of 1
// so a zero/oversized tick still heartbeats every tick rather than never
// (guaranteeing SOME baseline stream on a healthy daemon).
func heartbeatEveryN(tick, interval time.Duration) int {
	if tick <= 0 || interval <= 0 {
		return 1
	}
	n := int(math.Round(float64(interval) / float64(tick)))
	if n < 1 {
		return 1
	}
	return n
}

// shouldHeartbeat reports whether the given tick should emit a heartbeat: true
// on tick 0, everyN, 2*everyN, … and false in between. A degenerate everyN (< 1)
// is treated as 1 (heartbeat every tick) so the baseline stream never stalls.
func shouldHeartbeat(tickCount, everyN int) bool {
	if everyN < 1 {
		everyN = 1
	}
	return tickCount%everyN == 0
}

// heartbeatAttrs builds the small, all-string payload for the daemon.heartbeat
// log event: session counts by status (working/blocked/idle) summed across the
// tree, plan_tier, auto_resume, and the authoritative five_hour percentage when
// known. Kept intentionally small — a baseline liveness signal, not a full
// snapshot. Safe on a nil tree. An unknown (nil) FiveHourPct is omitted rather
// than reported as a false 0% (LogEvent also drops any empty-valued attr).
func heartbeatAttrs(tree *aggregate.Tree, planTier string, autoResumeEnabled bool) map[string]string {
	working, blocked, idle := 0, 0, 0
	var fiveHour *float64
	if tree != nil {
		for _, d := range tree.Dirs {
			working += d.WorkingN
			blocked += d.BlockedN
			idle += d.IdleN
		}
		fiveHour = tree.FiveHourPct
	}
	attrs := map[string]string{
		"sessions_working": strconv.Itoa(working),
		"sessions_blocked": strconv.Itoa(blocked),
		"sessions_idle":    strconv.Itoa(idle),
		"plan_tier":        planTier,
		"auto_resume":      strconv.FormatBool(autoResumeEnabled),
	}
	if fiveHour != nil {
		attrs["five_hour_pct"] = strconv.FormatFloat(*fiveHour, 'f', 1, 64)
	}
	return attrs
}

// Run is a thin compat wrapper preserving the original signature used by
// lifecycle_test.go and any caller that doesn't need RunOptions yet.
func Run(ctx context.Context, p Paths) error {
	return RunWith(ctx, RunOptions{Paths: p})
}

// blockToStoreBlock converts a usage.Block into a store.Block ready for
// persistence. capUSD is the per-5h-block soft cap, supplied by the caller from
// the Account so the conversion no longer looks up the cap from the concrete
// cost provider.
func blockToStoreBlock(b *usage.Block, capUSD float64, now time.Time) store.Block {
	sb := store.Block{
		BlockID:         b.ID,
		StartedAt:       b.StartTime,
		EndedAt:         b.EndTime,
		PlanCapUSD:      capUSD,
		TotalCostUSD:    b.CostUSD,
		LastProcessedAt: now,
		UpdatedAt:       now,
	}
	return sb
}

// blockToStoreBlockWithLimits is blockToStoreBlock plus the two window concepts
// the tree carries, so the persisted block carries them and the store->tree
// (GetState) path reflects the current reading:
//
//   - the daemon-pause usage window (tree.WindowResetsAt -> RateLimitResetsAt);
//   - the authoritative status-line rate_limits windows (ADR 0021 §5/§6).
//
// Unknown values (nil pointer / zero time on the tree) persist as nil — never
// 0 / 1970. The two concepts then use DIFFERENT on-conflict merge policies in
// the store (see internal/store/sqlite/block_store.go): the status-line columns
// COALESCE-preserve, because a nil reading there means "unknown/stale"; the
// pause window is last-write-wins, because a zero tree aggregate there means
// the known fact "no session is paused".
func blockToStoreBlockWithLimits(b *usage.Block, capUSD float64, now time.Time, tree *aggregate.Tree) store.Block {
	sb := blockToStoreBlock(b, capUSD, now)
	if tree == nil {
		return sb
	}
	// The daemon-pause usage window. aggregate.Build is the single place that
	// computes it (max RateLimitResetsAt across sessions); the per-session values
	// are not persisted, so this column is the ONLY carrier of the window on the
	// DB path. Without this write the served DaemonState.window_resets_at was
	// permanently unset and every operator-facing surface (TUI paused state,
	// "resuming in N:NN" banner, cmux sidebar) reported no window — pg2-tdzkq.
	if !tree.WindowResetsAt.IsZero() {
		t := tree.WindowResetsAt
		sb.RateLimitResetsAt = &t
	}
	sb.FiveHourPct = tree.FiveHourPct
	sb.SevenDayPct = tree.SevenDayPct
	if !tree.FiveHourResetsAt.IsZero() {
		t := tree.FiveHourResetsAt
		sb.FiveHourResetsAt = &t
	}
	if !tree.SevenDayResetsAt.IsZero() {
		t := tree.SevenDayResetsAt
		sb.SevenDayResetsAt = &t
	}
	if !tree.LimitsCapturedAt.IsZero() {
		t := tree.LimitsCapturedAt
		sb.LimitsCapturedAt = &t
	}
	return sb
}

// monitorLimits / monitorWeekly read the poller's Monitor projections through
// anonymous interfaces, so the daemon does not import internal/core/poller (which
// imports internal/daemon-adjacent core packages). They return the zero value
// when the poller does not expose them (e.g. a bare test fake), leaving the tree
// untouched. *Limits is daemon.Limits, an alias of limits.Limits, so the
// assertion matches the poller's MonitorLimits() *limits.Limits signature exactly.
func monitorLimits(p any) *Limits {
	if m, ok := p.(interface{ MonitorLimits() *Limits }); ok {
		return m.MonitorLimits()
	}
	return nil
}

func monitorWeekly(p any, now time.Time) *usage.WeeklyEntry {
	if m, ok := p.(interface {
		MonitorWeekly(time.Time) *usage.WeeklyEntry
	}); ok {
		return m.MonitorWeekly(now)
	}
	return nil
}

// applyLimits copies a LimitsSource reading onto the tree's account-global
// rate_limits fields (ADR 0021 §1/§5). A nil reading (no data captured yet) leaves
// the tree untouched, preserving whatever the store-materialised path already set.
// Every field is independently optional: an unknown value stays nil / zero — the
// reader never substitutes 0 / 1970.
func applyLimits(tree *aggregate.Tree, l *Limits) {
	if tree == nil || l == nil {
		return
	}
	if l.FiveHourPct != nil {
		tree.FiveHourPct = l.FiveHourPct
	}
	if l.SevenDayPct != nil {
		tree.SevenDayPct = l.SevenDayPct
	}
	if !l.FiveHourResetsAt.IsZero() {
		tree.FiveHourResetsAt = l.FiveHourResetsAt
	}
	if !l.SevenDayResetsAt.IsZero() {
		tree.SevenDayResetsAt = l.SevenDayResetsAt
	}
	if !l.CapturedAt.IsZero() {
		tree.LimitsCapturedAt = l.CapturedAt
	}
}

// blockLimitTrigger reports the AUTHORITATIVE account-level 5h-block limit-hit
// condition (ADR 0024 D3): the account-global authoritative FiveHourPct crossed
// 100%, OR any session is currently blocked on a terminal usage-limit. This
// replaces the retired ccusage CostUSD>=capUSD trigger — cost is not accurate
// enough (the operator sees 436% authoritative while cost-cap reads 0 hits).
func blockLimitTrigger(tree *aggregate.Tree) bool {
	if tree == nil {
		return false
	}
	if tree.FiveHourPct != nil && *tree.FiveHourPct >= 100 {
		return true
	}
	return anyUsageLimitBlocked(tree)
}

// weekLimitTrigger is the 7d counterpart of blockLimitTrigger (ADR 0024 R11):
// the authoritative SevenDayPct crossed 100%. SevenDayPct is a commonly-nil
// *float64 on this account, so it is NIL-GUARDED — a nil reading never fires and
// never panics (weekly essentially never fires here, which is expected).
func weekLimitTrigger(tree *aggregate.Tree) bool {
	return tree != nil && tree.SevenDayPct != nil && *tree.SevenDayPct >= 100
}

// anyUsageLimitBlocked reports whether any session is blocked on a terminal
// usage-limit. A per-session usage_limit is an authoritative limit-hit signal
// in its own right (ADR 0024 D3), independent of the account-global percentage.
func anyUsageLimitBlocked(tree *aggregate.Tree) bool {
	if tree == nil {
		return false
	}
	for _, sv := range tree.Sessions() {
		if sv.Session != nil && sv.Status == session.Blocked && sv.Blocker == session.UsageLimit {
			return true
		}
	}
	return false
}

// limitHitLatch fires an account-level limit-hit at most once per window (ADR
// 0024 R7), keyed on the authoritative window reset time. A changed reset time
// re-arms the latch (window rolled over); the zero-value latch is armed. Mirrors
// the once-per-window latches block.Tracker (retired) and nudger.LimitPauseFiredFor.
type limitHitLatch struct {
	fired    bool
	firedFor time.Time
}

// observe reports whether a limit-hit should fire THIS tick given the trigger
// condition `hit` and the current authoritative window `reset` time, updating
// the latch. It returns true at most once per distinct reset value; a changed
// reset re-arms. A reset value that never changes (e.g. the zero time when the
// authoritative reset is unknown) latches after the first fire until a new reset
// arrives.
func (l *limitHitLatch) observe(hit bool, reset time.Time) bool {
	if !hit {
		return false
	}
	if l.fired && !reset.Equal(l.firedFor) {
		l.fired = false // window rolled over → re-arm
	}
	if l.fired {
		return false
	}
	l.fired = true
	l.firedFor = reset
	return true
}

// weekToStoreWeek converts a usage.WeeklyEntry into a store.Week. The
// week window is anchored on the Monday (Period) and extends 7 days. capUSD is
// the per-week soft cap, supplied by the caller from the Account.
func weekToStoreWeek(w *usage.WeeklyEntry, capUSD float64, now time.Time) store.Week {
	// Parse the Monday anchor from "YYYY-MM-DD".
	var startedAt time.Time
	if t, err := time.Parse("2006-01-02", w.Period); err == nil {
		startedAt = t.UTC()
	}
	endedAt := startedAt.AddDate(0, 0, 7)
	sw := store.Week{
		WeekID:          w.Period,
		StartedAt:       startedAt,
		EndedAt:         endedAt,
		WeekCapUSD:      capUSD,
		TotalCostUSD:    w.TotalCost,
		LastProcessedAt: now,
		UpdatedAt:       now,
	}
	return sw
}
