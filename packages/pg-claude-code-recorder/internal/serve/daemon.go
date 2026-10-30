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
	// Procs is the process table (the ProcessTable seam; the real one when nil).
	Procs proc.System
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

	backfillDone atomic.Bool
	filesTotal   atomic.Int64
	filesCopied  atomic.Int64
	backfillAt   atomic.Int64 // unix ns the first sweep began

	usedMu     sync.Mutex
	usedBytes  int64
	usedAt     time.Time
	stop       chan struct{}
	stopOnce   sync.Once
	refreshing sync.WaitGroup
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
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	st, err := store.Open(cfg.StateDir, store.Options{Obs: o, Rebuild: opt.Rebuild})
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	d := &Daemon{opt: opt, cfg: cfg, obs: o, st: st, clock: opt.Clock, startedAt: opt.Clock.Now(), stop: make(chan struct{})}
	d.scope = scope.New(cfg.ClaudeDir, cfg.EndsDir())
	d.cp, err = copier.New(copier.Config{Store: st, Scope: d.scope, Clock: opt.Clock, Obs: o})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := d.cp.Recover(); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("recover copies: %w", err)
	}
	srcs := sources.All()
	d.prs = parser.New(parser.Config{Store: st, Sources: srcs, ClaudeDir: cfg.ClaudeDir, EndsDir: cfg.EndsDir(), Obs: o, Clock: opt.Clock, MaxLineBytes: cfg.ParseMaxLineBytes, FeedRetention: cfg.FeedRetention})
	d.qe = &query.Engine{DB: st.Read(), Catalog: sources.Catalog(srcs), Res: d.prs, Behind: func() bool { return !d.backfillDone.Load() }}
	d.fe = &feed.Engine{
		Q: d.qe, Store: st, Clock: opt.Clock, Obs: o, Retention: cfg.FeedRetention, WaitMax: cfg.WaitMax,
		MaxWaiters: cfg.WaitMaxWaiter, Behind: d.qe.Behind,
	}
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
	})
	if err := d.sett.Recover(); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("recover settle: %w", err)
	}
	d.ckpt = newFileCheckpoint(cfg.StateDir)
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

func (d *Daemon) backfill(files []watcher.FileInfo) {
	d.backfillAt.Store(d.clock.Now().UnixNano())
	d.filesTotal.Store(int64(len(files)))
	d.obs.Reg.Set(obs.MBackfillFiles, nil, float64(len(files)))
	sorted := append([]watcher.FileInfo(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ModTime.After(sorted[j].ModTime) })
	d.obs.Event("backfill_start", "files", fmt.Sprint(len(files)))
	skipped := false
	for _, f := range sorted {
		select {
		case <-d.stop:
			return
		default:
		}
		if d.sett.LowSpace() {
			// Low-space mode stops backfilling Conversations not yet copied.
			if fl, _ := d.st.FilesByCurrentPath(f.Path); len(fl) == 0 {
				skipped = true
				continue
			}
		}
		res := d.ingest(f.Path)
		d.filesCopied.Add(1)
		d.obs.Reg.Set(obs.MCopiedFiles, nil, float64(d.filesCopied.Load()))
		if res.Bytes > 0 && d.cfg.CopyThrottle > 0 {
			d.clock.Sleep(d.cfg.CopyThrottle)
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
	if err := d.srv.Listen(d.cfg.Socket); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	addr := d.opt.MetricsAddr
	if addr == "" {
		addr = d.cfg.MetricsAddr
	}
	if addr != "" {
		exp, err := exporter.Start(addr, d.obs.Reg, d.refreshGauges)
		if err != nil {
			d.obs.Log.Warn("metrics_exporter_failed", "err", err.Error())
		} else {
			d.exp = exp
			d.obs.Event("metrics_listening", "addr", exp.Addr())
		}
	}
	if err := d.w.Start(); err != nil {
		d.srv.Close()
		return fmt.Errorf("start watcher: %w", err)
	}
	d.tr.Startup()
	d.refreshing.Add(4)
	go d.settleLoop()
	go d.refreshLoop()
	go d.parseLoop()
	go d.trackLoop()
	d.obs.Event("serving", "socket", d.cfg.Socket, "claude_dir", d.cfg.ClaudeDir, "state_dir", d.cfg.StateDir,
		"version", d.opt.Version)
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
			if err := d.sett.Tick(); err != nil {
				d.obs.Log.Warn("settle_pass_failed", "err", err.Error())
			}
		}
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

// refreshGauges sets the heartbeat (the watcher's last successful sweep) and the
// state directory's free and used bytes. Used bytes walk the directory, so they
// are recomputed at most once a minute.
func (d *Daemon) refreshGauges() {
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
	d.obs.Reg.Set(obs.MStateUsedBytes, nil, float64(used))
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
