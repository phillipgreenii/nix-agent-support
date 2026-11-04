// Package serve composes the recorder: the store, the copier, the watcher with
// its checkpoint, the socket listener and the metrics exporter, and implements
// the status and coverage operations over them.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/exporter"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Options configures a Daemon.
type Options struct {
	Config  config.Config
	Logger  *slog.Logger // nil discards
	Clock   clock.Clock  // nil is the real clock
	Version string
	// NoEvents leaves the watcher sweep-only (tests drive it with Wake/Rescan).
	NoEvents bool
	// Metrics, when non-empty, overrides Config.MetricsAddr.
	MetricsAddr string
	// Rebuild forces index.db to be rebuilt from the journal (serve --rebuild,
	// the rebuild command); it is also the way past a newer schema_version.
	Rebuild bool
	// BulkRebuild (with Rebuild) runs the offline rebuild command in bulk mode
	// (store.Options.BulkRebuild): the caller MUST call Store().FinishRebuild
	// after a successful ParseAll. serve --rebuild does not use it.
	BulkRebuild bool
	// Procs is the process table (the ProcessTable seam; the real one when nil).
	Procs proc.System
	// StoreDriver is the database/sql driver name of the store (tests register a
	// counting wrapper); empty means the SQLite driver.
	StoreDriver string
}

// Daemon is the composed recorder.
type Daemon struct {
	opt   Options
	cfg   config.Config
	obs   *obs.Obs
	st    *store.Store
	cp    *copier.Copier
	scope *scope.Scope
	w     *watcher.Watcher
	ckpt  *fileCheckpoint
	prs   *parser.Parser
	qe    *query.Engine
	fe    *feed.Engine
	srv   *server.Server
	exp   *exporter.Server
	eng   *lifecycle.Engine
	tr    *lifecycle.Tracker
	sett  *settle.Settler
	clock clock.Clock

	startedAt time.Time
	su        *startupTimer
	// tickPhases returns the phase breakdown of the settle tick that just ran.
	tickPhases func() settle.TickPhases
	// tickStartNS is when the running settle tick began (clock UnixNano), 0 when
	// none runs; the in-flight gauge and the stuck-tick log read it.
	tickStartNS atomic.Int64

	backfillDone atomic.Bool
	filesTotal   atomic.Int64
	filesCopied  atomic.Int64
	backfillAt   atomic.Int64 // unix ns the first sweep began

	// lastCkptWarn is touched only by the WAL checkpoint loop.
	lastCkptWarn time.Time
	usedMu       sync.Mutex
	usedBytes    int64
	usedAt       time.Time
	stop         chan struct{}
	stopOnce     sync.Once
	refreshing   sync.WaitGroup

	// statusMu guards statusSnap, the cached figures opStatus serves.
	statusMu     sync.Mutex
	statusSnap   *statusSnap
	statusLoopOn atomic.Bool
	statusReady  chan struct{} // closed once the loop has stored its first snapshot
	statusFirst  sync.Once
}

// Open builds the daemon: it opens the store, probes the copy method, recovers
// the copies and builds the watcher. Nothing listens until Start.
func Open(opt Options) (*Daemon, error) {
	cfg := opt.Config
	// FSEvents and inotify report real paths, so a Claude directory reached
	// through a symlink (or /tmp on darwin) would never match the scope.
	cfg.ClaudeDir = realPath(cfg.ClaudeDir)
	if cfg.ControlStateDir != "" {
		cfg.ControlStateDir = realPath(cfg.ControlStateDir)
	}
	if opt.Clock == nil {
		opt.Clock = clock.Real{}
	}
	o := obs.New(opt.Logger)
	o.DisableCapture()
	su := newStartupTimer(o, opt.Clock)
	endPhase := su.begin("state_dir", false)
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	endPhase()
	st, err := store.Open(cfg.StateDir, store.Options{Phase: su.storeHook, Obs: o, Rebuild: opt.Rebuild, BulkRebuild: opt.BulkRebuild, DriverName: opt.StoreDriver, ReadTimeout: cfg.QueryTimeout, ReadCacheKiB: cfg.ReadCacheMiB << 10, ReadMmapBytes: readMmapBytes(cfg.ReadMmapMiB)})
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	d := &Daemon{opt: opt, cfg: cfg, obs: o, st: st, clock: opt.Clock, startedAt: opt.Clock.Now(), su: su, stop: make(chan struct{}), statusReady: make(chan struct{})}
	d.scope = scope.New(cfg.ClaudeDir, cfg.EndsDir())
	d.cp, err = copier.New(copier.Config{Store: st, Scope: d.scope, Clock: opt.Clock, Obs: o})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	endPhase = su.begin("copier_recover", true)
	err = d.cp.Recover()
	endPhase()
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("recover copies: %w", err)
	}
	srcs := sources.All()
	d.prs = parser.New(parser.Config{Store: st, Sources: srcs, ClaudeDir: cfg.ClaudeDir, EndsDir: cfg.EndsDir(), Obs: o, Clock: opt.Clock, MaxLineBytes: cfg.ParseMaxLineBytes, FeedRetention: cfg.FeedRetention})
	d.qe = &query.Engine{DB: st.Read(), Catalog: sources.Catalog(srcs), ClaudeDir: cfg.ClaudeDir, Res: d.prs, SessionRef: st.SessionRefReady, Behind: func() bool { return !d.backfillDone.Load() }}
	d.fe = &feed.Engine{
		Q: d.qe, Store: st, Clock: opt.Clock, Obs: o, Retention: cfg.FeedRetention, WaitMax: cfg.WaitMax,
		MaxWaiters: cfg.WaitMaxWaiter, Behind: d.qe.Behind,
	}
	d.qe.FilterFields = d.noteFilterFields
	st.OnWrite(d.fe.Notify)
	if opt.Procs == nil {
		opt.Procs = proc.NewReal()
	}
	d.eng = &lifecycle.Engine{St: st, ControlDir: cfg.ControlStateDir, Now: opt.Clock.Now}
	d.tr = lifecycle.NewTracker(lifecycle.TrackerConfig{
		Store: st, Sys: opt.Procs, Obs: o, Clock: opt.Clock, Engine: d.eng, Sync: func() { _ = d.prs.Drain() },
		ClaudeDir: cfg.ClaudeDir, ControlDir: cfg.ControlStateDir, GraceEnable: cfg.GraceEnable, GraceWindow: cfg.GraceWindow,
	})
	d.sett = settle.New(settle.Config{
		Store: st, Copier: d.cp, Parser: d.prs, Live: d.eng.LiveSessions, Clock: opt.Clock, Obs: o,
		ClaudeDir: cfg.ClaudeDir, After: cfg.SettleAfter, Recheck: cfg.HeldRecheck, HeldAlertAfter: cfg.HeldMaxAge,
		MinFreeBytes: cfg.MinFreeBytes, MaxLineBytes: cfg.ParseMaxLineBytes, Free: settle.FreeFunc(FreeBytes),
		RemoveEnable: cfg.RemoveEnable, RemoveAfter: cfg.RemoveAfter, MaxPerRun: cfg.RemoveMaxPerRun, MaxPerHour: cfg.RemoveMaxPerHour,
		HoldRate: cfg.FormatHoldRate,
	})
	d.tickPhases = d.sett.LastPhases
	endPhase = su.begin("doctor_gauges", false)
	d.loadDoctorGauges()
	endPhase()
	endPhase = su.begin("settle_recover", true)
	err = d.sett.Recover()
	endPhase()
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("recover settle: %w", err)
	}
	d.ckpt = newFileCheckpoint(cfg.StateDir)
	endPhase = su.begin("watcher_new", false)
	d.w, err = watcher.New(watcher.Config{
		Roots:         cfg.Roots(),
		Filter:        d.filter,
		Sink:          d.sink,
		OnSweep:       d.onSweep,
		Checkpoint:    d.ckpt,
		SweepInterval: cfg.SweepInterval,
		Now:           opt.Clock.Now,
		Logger:        o.Log,
		DisableEvents: opt.NoEvents,
	})
	endPhase()
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	return d, nil
}

// Obs returns the daemon's registry and logger.
func (d *Daemon) Obs() *obs.Obs { return d.obs }

// Tracker returns the Process tracker (tests drive its periodic work).
func (d *Daemon) Tracker() *lifecycle.Tracker { return d.tr }

// Settler returns the settle stage (tests drive its passes).
func (d *Daemon) Settler() *settle.Settler { return d.sett }

// Parser returns the parse stage (tests drain it).
func (d *Daemon) Parser() *parser.Parser { return d.prs }

// Store returns the open store (tests and later phases).
func (d *Daemon) Store() *store.Store { return d.st }

// Watcher returns the watcher (the Wake/Rescan seams).
func (d *Daemon) Watcher() *watcher.Watcher { return d.w }

// MetricsAddr returns the exporter's bound address, "" when it is off.
func (d *Daemon) MetricsAddr() string {
	if d.exp == nil {
		return ""
	}
	return d.exp.Addr()
}

// filter is the watcher's injected path filter: the scope matcher plus the
// configured block-list additions (base-name globs).
func (d *Daemon) filter(p string) bool {
	if !d.scope.Match(p).Ingest {
		return false
	}
	base := filepath.Base(p)
	for _, pat := range d.cfg.BlockList {
		if ok, _ := path.Match(pat, base); ok {
			return false
		}
	}
	return true
}

func (d *Daemon) ingest(p string) copier.Result {
	res, err := d.cp.Ingest(p)
	return d.afterIngest(p, res, err)
}

// afterIngest logs a failed ingest and tells the parser and the lifecycle
// tracker about a copied path.
func (d *Daemon) afterIngest(p string, res copier.Result, err error) copier.Result {
	if err != nil {
		d.obs.Log.Warn("ingest_failed", "path", p, "err", err.Error())
	}
	if res.File != 0 {
		d.prs.Notify(res.File)
	}
	if src, _ := d.scope.Classify(p); src == scope.SrcRegistry {
		d.tr.Observe(p)
	}
	return res
}

func (d *Daemon) sink(e watcher.Event) { d.ingest(e.Path) }

// onSweep runs under the watcher's lock before the sweep's events. It records
// deletions (a missing path with no file of the same identity elsewhere), and
// on the first sweep copies newest first with a throttle, so the most recent
// Conversations are safe first and the laptop stays usable.
func (d *Daemon) onSweep(files []watcher.FileInfo) {
	found := make(map[copier.Identity]bool, len(files))
	for _, f := range files {
		var birth int64
		if !f.Birth.IsZero() {
			birth = f.Birth.UnixNano()
		}
		found[copier.Identity{Dev: f.Dev, Ino: f.Ino, Birth: birth}] = true
	}
	if !d.backfillDone.Load() {
		d.backfill(files)
	}
	if err := d.cp.SweepMissing(found); err != nil {
		d.obs.Log.Warn("sweep_missing_failed", "err", err.Error())
	}
	d.notifyDeletedSnapshots()
}

// notifyDeletedSnapshots asks the parser to look at snapshot files that a sweep
// found deleted, so their deletion records are written.
func (d *Daemon) notifyDeletedSnapshots() {
	files, err := d.st.AllFiles()
	if err != nil {
		return
	}
	for _, f := range files {
		if f.Mode == "snapshot" && f.State == "deleted" && d.prs.HasSource(f.Source) {
			d.prs.Notify(f.ID)
		}
	}
}

// backfillBatch is the number of files the first backfill copies per group
// commit. The per-file work (clone, fsync, rename) is unchanged; only the
// journal commit and directory sync are shared.
const backfillBatch = 64

func (d *Daemon) backfill(files []watcher.FileInfo) {
	d.backfillAt.Store(d.clock.Now().UnixNano())
	d.filesTotal.Store(int64(len(files)))
	d.obs.Reg.Set(obs.MBackfillFiles, nil, float64(len(files)))
	sorted := append([]watcher.FileInfo(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ModTime.After(sorted[j].ModTime) })
	d.obs.Event("backfill_start", "files", fmt.Sprint(len(files)))
	skipped := false
	// Files are copied in batches that share one journal commit and one directory
	// sync (copier.IngestBatch); the newest-first order and the low-space check
	// are unchanged. A batch is durable as a unit: a crash inside one leaves the
	// files of that batch uncommitted and the next backfill copies them again.
	for start := 0; start < len(sorted); start += backfillBatch {
		select {
		case <-d.stop:
			return
		default:
		}
		var paths []string
		for _, f := range sorted[start:min(start+backfillBatch, len(sorted))] {
			if d.sett.LowSpace() {
				// Low-space mode stops backfilling Conversations not yet copied.
				if fl, _ := d.st.FilesByCurrentPath(f.Path); len(fl) == 0 {
					skipped = true
					continue
				}
			}
			paths = append(paths, f.Path)
		}
		for _, br := range d.cp.IngestBatch(paths) {
			res := d.afterIngest(br.Path, br.Result, br.Err)
			d.filesCopied.Add(1)
			d.obs.Reg.Set(obs.MCopiedFiles, nil, float64(d.filesCopied.Load()))
			if res.Bytes > 0 && d.cfg.CopyThrottle > 0 {
				d.clock.Sleep(d.cfg.CopyThrottle)
			}
		}
	}
	if skipped {
		return
	}
	d.backfillDone.Store(true)
	d.tr.StartupDone()
	d.obs.Reg.Set(obs.MCaughtUp, nil, 1)
	d.obs.Event("backfill_done", "files", fmt.Sprint(len(files)))
}

// Start opens the listener and the exporter and starts the watcher.
func (d *Daemon) Start() error {
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	d.srv.Register("status", server.KindQuery, d.opStatus)
	d.srv.Register("coverage", server.KindQuery, d.opCoverage)
	d.srv.Register("query", server.KindQuery, d.opQuery)
	d.srv.Register("search", server.KindQuery, d.opSearch)
	d.srv.Register("feed", server.KindFeed, d.fe.Feed)
	d.srv.Register("snapshot", server.KindQuery, d.fe.Snapshot)
	d.srv.Register("raw", server.KindQuery, d.fe.Raw)
	d.srv.Register("conversation.show", server.KindQuery, d.opConversationShow)
	d.srv.Register("conversation.list", server.KindQuery, d.opConversationList)
	d.srv.Register("process.show", server.KindQuery, d.opProcessShow)
	d.srv.Register("process.list", server.KindQuery, d.opProcessList)
	d.srv.Register("held", server.KindQuery, d.opHeld)
	d.srv.Register("where", server.KindQuery, d.opWhere)
	d.srv.Register("remove", server.KindQuery, d.opRemove)
	d.srv.Register("extract", server.KindQuery, d.opExtract)
	d.srv.Register("doctor", server.KindQuery, d.opDoctor)
	endPhase := d.su.begin("listener_bind", false)
	if err := d.srv.Listen(d.cfg.Socket); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	endPhase()
	addr := d.opt.MetricsAddr
	if addr == "" {
		addr = d.cfg.MetricsAddr
	}
	if addr != "" {
		endPhase = d.su.begin("metrics_listen", false)
		exp, err := exporter.Start(addr, d.obs.Reg, d.scrapeRefresh)
		if err != nil {
			d.obs.Log.Warn("metrics_exporter_failed", "err", err.Error())
		} else {
			d.exp = exp
			d.obs.Event("metrics_listening", "addr", exp.Addr())
		}
		endPhase()
	}
	endPhase = d.su.begin("watcher_start", true)
	if err := d.w.Start(); err != nil {
		d.srv.Close()
		return fmt.Errorf("start watcher: %w", err)
	}
	endPhase()
	endPhase = d.su.begin("tracker_startup", true)
	d.tr.Startup()
	endPhase()
	d.refreshing.Add(7)
	d.statusLoopOn.Store(true)
	go d.statusLoop()
	go d.walCheckpointLoop()
	go d.settleLoop()
	go d.doctorLoop()
	go d.refreshLoop()
	go d.parseLoop()
	go d.trackLoop()
	d.obs.Event("serving", "socket", d.cfg.Socket, "claude_dir", d.cfg.ClaudeDir, "state_dir", d.cfg.StateDir,
		"version", d.opt.Version)
	d.su.complete()
	return nil
}

// parseLoop catches up every copy on start (and re-parses what an older parser
// version stored), then parses each file as the copier reports new bytes.
func (d *Daemon) parseLoop() {
	defer d.refreshing.Done()
	if err := d.prs.ParseAll(); err != nil {
		d.obs.Log.Warn("parse_all_failed", "err", err.Error())
	}
	for {
		select {
		case <-d.stop:
			return
		case <-d.prs.Wake():
			// Group commit: let the wake-ups of a burst settle, then parse every
			// dirty file in one transaction.
			if !d.prs.Coalesce(d.stop) {
				return
			}
			if err := d.prs.Drain(); err != nil {
				d.obs.Log.Warn("parse_failed", "err", err.Error())
			}
		}
	}
}

// settleLoop runs the settle pass once a minute, after the first parse.
func (d *Daemon) settleLoop() {
	defer d.refreshing.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			d.runTick(d.sett.Tick)
		}
	}
}

// settleTickSlow is the duration above which a settle tick logs a warning (the
// tick interval).
const settleTickSlow = time.Minute

// runTick runs one settle pass through step and is the only place that records
// the tick's timing and outcome: the duration histogram, the run counter, the
// work counters, the last-duration and last-timestamp gauges, and one
// settle_tick event (duration_ms in milliseconds; the metrics are in seconds).
func (d *Daemon) runTick(step func() (settle.TickStats, error)) {
	start := d.clock.Now()
	d.tickStartNS.Store(start.UnixNano())
	watchdog := time.AfterFunc(settleTickSlow, d.logStuckTick)
	stats, err := step()
	watchdog.Stop()
	d.tickStartNS.Store(0)
	d.setTickInFlight()
	end := d.clock.Now()
	dur := end.Sub(start)
	outcome := obs.OutcomeOK
	if err != nil {
		outcome = obs.OutcomeError
		d.obs.Log.Warn("settle_pass_failed", "err", err.Error())
	}
	reg := d.obs.Reg
	reg.Observe(obs.MSettleTickDuration, dur.Seconds())
	reg.Add(obs.MLoopRuns, map[string]string{"loop": obs.LoopSettleTick, "outcome": outcome}, 1)
	reg.Add(obs.MSettleTickExamined, nil, float64(stats.Examined))
	reg.Add(obs.MSettleTickTransitions, nil, float64(stats.Transitions))
	reg.Set(obs.MSettleTickLastDuration, nil, dur.Seconds())
	reg.Set(obs.MSettleTickLastTimestamp, nil, float64(end.UnixNano())/1e9)
	d.obs.Event(obs.EvSettleTick,
		"outcome", outcome,
		"duration_ms", strconv.FormatInt(dur.Milliseconds(), 10),
		"examined", strconv.Itoa(stats.Examined),
		"unchanged", strconv.Itoa(stats.Unchanged),
		"settled_now", strconv.Itoa(stats.SettledNow),
		"transitions", strconv.Itoa(stats.Transitions))
	if dur > settleTickSlow {
		attrs := []any{"duration_ms", dur.Milliseconds(), "examined", stats.Examined}
		if d.tickPhases != nil {
			p := d.tickPhases()
			attrs = append(attrs,
				"reads_ms", p.Reads.Milliseconds(), "prefilter_ms", p.Prefilter.Milliseconds(),
				"evaluate_ms", p.Evaluate.Milliseconds(), "removal_ms", p.Removal.Milliseconds(),
				"refresh_ms", p.Refresh.Milliseconds())
		}
		d.obs.Log.Warn(obs.EvSettleTickSlow, attrs...)
	}
}

// tickInFlightAge is how long the running settle tick has been going (it
// includes its wait for the run lock), 0 when none is.
func (d *Daemon) tickInFlightAge() time.Duration {
	ns := d.tickStartNS.Load()
	if ns == 0 {
		return 0
	}
	return d.clock.Now().Sub(time.Unix(0, ns))
}

// setTickInFlight sets settle_tick_in_flight_seconds: the age of the running
// tick, so a tick that never finishes is visible while it is stuck (the
// last-timestamp gauge only moves when a tick ends).
func (d *Daemon) setTickInFlight() {
	d.obs.Reg.Set(obs.MSettleTickInFlight, nil, d.tickInFlightAge().Seconds())
}

// logStuckTick is the watchdog timer's callback: a tick still running after
// settleTickSlow is logged while it is stuck, not only when it ends.
func (d *Daemon) logStuckTick() {
	if age := d.tickInFlightAge(); age > 0 {
		d.obs.Log.Warn(obs.EvSettleTickStuck, "in_flight_ms", age.Milliseconds())
	}
}

// trackLoop refreshes last-seen-alive and does the control state directory's
// periodic work.
func (d *Daemon) trackLoop() {
	defer d.refreshing.Done()
	every := d.cfg.LivenessInterval
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			d.tr.Tick()
			d.tr.Poll()
		}
	}
}

func (d *Daemon) refreshLoop() {
	defer d.refreshing.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			d.refreshGauges()
		}
	}
}

// scrapeRefresh is what a /metrics scrape runs before rendering: only the
// process-local figures (CPU, Go runtime, WAL file size). It MUST NOT query the
// store or walk the state directory: those wait on the SQLite connection the
// settle tick holds and made a scrape take 14 s or hang. The heavier gauges
// come from refreshGauges, which the refresh loop runs every 5 seconds.
func (d *Daemon) scrapeRefresh() {
	d.setTickInFlight()
	collectRuntimeMetrics(d.obs.Reg, filepath.Join(d.cfg.StateDir, store.DBName+"-wal"))
	if d.st != nil {
		collectPoolMetrics(d.obs.Reg, d.st.PoolStats())
	}
}

// refreshGauges sets the heartbeat (the watcher's last successful sweep) and the
// state directory's free and used bytes. Used bytes walk the directory, so they
// are recomputed at most once a minute.
func (d *Daemon) refreshGauges() {
	d.setTickInFlight()
	start := time.Now()
	defer func() {
		d.obs.Reg.Observe(obs.MRefreshGaugesDuration, time.Since(start).Seconds())
		d.obs.Reg.Add(obs.MLoopRuns, map[string]string{"loop": obs.LoopRefreshGauges, "outcome": obs.OutcomeOK}, 1)
	}()
	collectRuntimeMetrics(d.obs.Reg, filepath.Join(d.cfg.StateDir, store.DBName+"-wal"))
	if d.st != nil {
		collectPoolMetrics(d.obs.Reg, d.st.PoolStats())
	}
	d.prs.UpdateGauges()
	if t := d.w.LastSweep(); !t.IsZero() {
		d.obs.Reg.Set(obs.MHeartbeat, nil, float64(t.UnixNano())/1e9)
	}
	if free, err := FreeBytes(d.cfg.StateDir); err == nil {
		d.obs.Reg.Set(obs.MStateFreeBytes, nil, float64(free))
	}
	d.usedMu.Lock()
	if d.usedAt.IsZero() || time.Since(d.usedAt) > time.Minute {
		d.usedBytes = DirBytes(d.cfg.StateDir)
		d.usedAt = time.Now()
	}
	used := d.usedBytes
	d.usedMu.Unlock()
	// The quarantined files are still on disk until their unlink: they count in
	// the used figure.
	qb, _ := d.obs.Reg.Gauge(obs.MQuarantineBytes, nil)
	d.obs.Reg.Set(obs.MStateUsedBytes, nil, float64(used)+qb)
}

// Close stops everything and flushes the checkpoint.
func (d *Daemon) Close() error {
	d.stopOnce.Do(func() { close(d.stop) })
	if d.srv != nil {
		d.srv.Close()
	}
	if d.exp != nil {
		d.exp.Close()
	}
	var errs []error
	errs = append(errs, d.w.Close())
	d.refreshing.Wait()
	d.tr.Close()
	errs = append(errs, d.ckpt.Flush())
	errs = append(errs, d.st.Close())
	return errors.Join(errs...)
}

// Run starts the daemon and blocks until ctx ends, then closes it.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Start(); err != nil {
		_ = d.Close()
		return err
	}
	<-ctx.Done()
	return d.Close()
}

func (d *Daemon) hello() any {
	return wire.Hello{
		Service: serviceName, Version: d.opt.Version, ProtocolVersion: wire.ProtocolVersion,
		SchemaVersion: d.st.SchemaVersion(), Epoch: d.st.Epoch(), Capabilities: d.srv.Operations(),
		CaughtUp: d.backfillDone.Load(), Degraded: !d.backfillDone.Load(),
	}
}

// realPath resolves symlinks in p when it exists, else returns p unchanged.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// readMmapBytes converts the configured memory-map ceiling (MiB) to the store
// option: 0 MiB disables mapping, which the store spells as a negative size.
func readMmapBytes(mib int) int64 {
	if mib <= 0 {
		return -1
	}
	return int64(mib) << 20
}
