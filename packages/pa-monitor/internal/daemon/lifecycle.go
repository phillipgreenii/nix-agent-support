package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/gofrs/flock"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/block"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/week"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store"
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
// surrogate block/week ids after each ccusage upsert so that the next
// Snapshot call can attach contributions to the correct parent rows.
type BlockWeekIDSetter interface {
	SetActiveBlockID(id int64)
	SetActiveWeekID(id int64)
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
	// RuntimePath is the runtime.json file path. Empty disables persistence
	// of caffeinate toggle changes from Caffeinate RPC.
	RuntimePath string
	// WeeklyFn fetches the current week entry. Nil → never polled.
	WeeklyFn func(ctx context.Context) (*ccusage.WeeklyEntry, error)
	// PlanTier is forwarded as the `plan_tier` attribute on emitted
	// limit-hit events/counters.
	PlanTier string
	// WeeklyEvery controls how often WeeklyFn is invoked relative to
	// the main tick. 0 means once per tick.
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
	// ccusage tick. The returned surrogate ids are assigned to the poller's
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
	defer lis.Close()

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

	version := opts.Version
	if version == "" {
		version = "dev"
	}
	_, stop := serve(lis, state, version, opts.PlanTier, opts.AutoResumeMessage, opts.WriteService, opts.BridgeRegistry, opts.CmuxAncestor)
	defer stop()

	defer opts.Emitter.Shutdown(context.Background())

	// One-shot migration: copy runtime.json toggles into the DB, then delete
	// the file. Runs before NewWatermarkStore so that, on first startup after
	// migration, the WatermarkStore sees an absent file (empty state) rather
	// than the stale JSON. Best-effort — log the error but continue startup.
	if opts.RuntimePath != "" && opts.WriteService != nil {
		if err := MigrateRuntimeJSON(ctx, opts.RuntimePath,
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
		sig := &SignalerAdapter{Signalers: opts.NudgerSignalers}
		var nr nudger.NudgeRecorder
		if opts.WriteService != nil && opts.DB != nil {
			nr = &nudgeRecorder{ws: opts.WriteService, db: opts.DB}
		}
		n := nudger.New(sig, watermarks, nr)
		n.LoadStore(watermarks.LoadIntents())
		state.mu.Lock()
		state.nudger = n
		state.watermarks = watermarks
		state.mu.Unlock()
		state.setPendingNudgeQueue(n)
	}

	// Wire tracker callbacks to emitter counters/events.
	if opts.BlockTracker != nil && opts.Emitter != nil {
		opts.BlockTracker.OnLimitHit = func() {
			opts.Emitter.RecordBlockLimitHit(map[string]string{
				"plan_tier": opts.PlanTier,
				"block.id":  opts.BlockTracker.ID(),
			})
		}
	}
	if opts.WeekTracker != nil && opts.Emitter != nil {
		opts.WeekTracker.OnLimitHit = func() {
			opts.Emitter.RecordWeekLimitHit(map[string]string{
				"plan_tier": opts.PlanTier,
				"week.id":   opts.WeekTracker.ID(),
				"source":    "computed",
			})
		}
	}

	tick := opts.Tick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	capLimit := opts.LabelCap
	if capLimit <= 0 {
		capLimit = 50
	}
	labelCap := labels.NewCardinalityCap(capLimit)
	labelCache := map[string]labels.Set{}

	// previousErrors tracks the last-seen LastError.At per session so we
	// can detect newly advanced errors and fire the api_error.observed counter.
	previousErrors := map[string]time.Time{}

	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			tickCount++
			if opts.Poller == nil {
				// no poller — still advance the (toggle-driven) caffeinate
				// tick to honour RPC-driven on/off requests
				if opts.Caffeinate != nil {
					// re-read user toggle from shared state in case Caffeinate RPC changed it
					opts.Caffeinate.SetToggle(state.isCaffeinateOn())
					opts.Caffeinate.Tick(false)
				}
				continue
			}
			tree, _, err := opts.Poller.Snapshot(ctx)
			if err != nil {
				continue
			}
			fetchWeek := opts.WeeklyFn != nil && (opts.WeeklyEvery <= 0 || tickCount%opts.WeeklyEvery == 0)
			if fetchWeek {
				if w, err := opts.WeeklyFn(ctx); err == nil && w != nil {
					tree.ActiveWeek = w
				}
			}

			// Persist the active block and week to the DB, then propagate
			// their surrogate ids to the poller so contribution upserts in the
			// next Snapshot have valid parent ids.
			if opts.WriteService != nil {
				if tree.ActiveBlock != nil {
					nowUTC := time.Now().UTC()
					b := blockToStoreBlock(tree.ActiveBlock, opts.PlanTier, nowUTC)
					if blockID, err := opts.WriteService.UpsertBlock(ctx, b); err == nil {
						if setter, ok := opts.Poller.(BlockWeekIDSetter); ok {
							setter.SetActiveBlockID(blockID)
						}
					}
				}
				if tree.ActiveWeek != nil {
					nowUTC := time.Now().UTC()
					w := weekToStoreWeek(tree.ActiveWeek, opts.PlanTier, nowUTC)
					if weekID, err := opts.WriteService.UpsertWeek(ctx, w); err == nil {
						if setter, ok := opts.Poller.(BlockWeekIDSetter); ok {
							setter.SetActiveWeekID(weekID)
						}
					}
				}
			}

			if opts.BlockTracker != nil {
				opts.BlockTracker.Update(tree.ActiveBlock)
			}
			if opts.WeekTracker != nil {
				opts.WeekTracker.Update(tree.ActiveWeek)
			}
			anyWorking := false
			for _, d := range tree.Dirs {
				if d.WorkingN > 0 {
					anyWorking = true
					break
				}
			}
			if opts.Caffeinate != nil {
				toggleOn := state.isCaffeinateOn()
				opts.Caffeinate.SetToggle(toggleOn)
				prevState := opts.Caffeinate.State()
				opts.Caffeinate.Tick(anyWorking)
				newState := opts.Caffeinate.State()
				// active is true when the subprocess is running OR when the
				// user toggle is on but the manager is waiting for agents to
				// start before spawning (StateOff + toggle=true). Without the
				// toggleOn guard, a tick with anyWorking=false would reset
				// caffeinateActive to false immediately after the user flips
				// the toggle on, causing the TUI indicator to revert.
				active := newState != caffeinate.StateOff || toggleOn
				cause := ""
				if active {
					if toggleOn {
						cause = "manual"
					}
					if anyWorking {
						cause = "agents_active"
					}
				}
				state.setCaffeinateActive(active, cause)
				opts.Emitter.RecordCaffeinateActive(active, map[string]string{"plan_tier": opts.PlanTier})
				if prevState == caffeinate.StateOff && newState != caffeinate.StateOff {
					opts.Emitter.RecordCaffeinateRound(map[string]string{"cause": cause})
				}
				if prevState == caffeinate.StateArmedCountdown && newState == caffeinate.StateOff && !anyWorking {
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
			updateGauges(opts.Emitter, tree, opts.PlanTier, opts.Detectors, opts.Decorators, labelCap, labelCache)
			updateSessionInfo(opts.Emitter, tree, opts.PlanTier, opts.Detectors, opts.Decorators, labelCap, labelCache)
			// Drop stale label cache entries for sessions that vanished.
			pruneLabelCache(labelCache, tree)

			// Emit api_error.observed for each newly-seen error, and
			// snapshot sessions.errored gauge per kind.
			emitErrorMetrics(opts.Emitter, tree, previousErrors)

			// Run nudger tick after tree is built and before publishing to clients.
			state.mu.RLock()
			n := state.nudger
			wm := state.watermarks
			state.mu.RUnlock()
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

			if opts.TreeObserver != nil {
				opts.TreeObserver(tree)
			}
		}
	}
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
	decorators []*labels.Decorator,
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
	decorators []*labels.Decorator,
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
	decorators []*labels.Decorator,
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
		if sv.Status == session.Dormant {
			continue
		}
		errKind := ""
		if sv.LastError != nil && sv.LastError.IsTerminal {
			errKind = string(sv.LastError.Kind)
		}
		ls := labelsForSession(sv, detectors, decorators, cap, labelCache)
		// Pass plan_tier through as a baseline label so the dashboard's
		// $plan_tier filter applies to this gauge too.
		ls["plan_tier"] = planTier
		rows = append(rows, otel.SessionInfo{
			SessionID:    sv.SessionID,
			SessionName:  sv.Session.Name,
			Cwd:          sv.Cwd,
			TerminalHost: session.TerminalAbbrev(sv.TerminalHost),
			Status:       session.Status(sv.Status).String(),
			Model:        sv.SessionEnrichment.Model,
			ErrorKind:    errKind,
			Tokens:       int64(sv.SessionEnrichment.SessionTokens),
			CostUSD:      sv.SessionEnrichment.CostUSD,
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
func labelsForSession(
	sv *aggregate.SessionView,
	detectors []labels.Detector,
	decorators []*labels.Decorator,
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
		cached = labels.Set{}
		for _, d := range detectors {
			cached = cached.Merge(d.Detect(s))
		}
		for _, dec := range decorators {
			cached = cached.Merge(dec.Detect(s))
		}
		if cap != nil {
			for k, v := range cached {
				cached[k] = cap.Cap(k, v)
			}
		}
		cache[sv.SessionID] = cached
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
	erroredCounts := map[string]int{}
	for _, dir := range tree.Dirs {
		for _, sv := range dir.Sessions {
			le := sv.LastError
			if le == nil {
				continue
			}
			kind := string(le.Kind)
			erroredCounts[kind]++
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
	e.RecordSessionsErrored(erroredCounts)
}

// Run is a thin compat wrapper preserving the original signature used by
// lifecycle_test.go and any caller that doesn't need RunOptions yet.
func Run(ctx context.Context, p Paths) error {
	return RunWith(ctx, RunOptions{Paths: p})
}

// blockToStoreBlock converts a ccusage.Block into a store.Block ready for
// persistence. PlanCapUSD is derived from the plan tier.
func blockToStoreBlock(b *ccusage.Block, planTier string, now time.Time) store.Block {
	sb := store.Block{
		BlockID:         b.ID,
		StartedAt:       b.StartTime,
		EndedAt:         b.EndTime,
		PlanCapUSD:      ccusage.PlanCapUSD(planTier),
		TotalCostUSD:    b.CostUSD,
		LastProcessedAt: now,
		UpdatedAt:       now,
	}
	return sb
}

// weekToStoreWeek converts a ccusage.WeeklyEntry into a store.Week. The
// week window is anchored on the Monday (Period) and extends 7 days.
func weekToStoreWeek(w *ccusage.WeeklyEntry, planTier string, now time.Time) store.Week {
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
		WeekCapUSD:      ccusage.WeekCapUSD(planTier),
		TotalCostUSD:    w.TotalCost,
		LastProcessedAt: now,
		UpdatedAt:       now,
	}
	return sw
}
