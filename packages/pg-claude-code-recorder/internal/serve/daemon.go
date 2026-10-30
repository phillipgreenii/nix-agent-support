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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
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
	srv   *server.Server
	exp   *exporter.Server
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
	st, err := store.Open(cfg.StateDir, store.Options{Obs: o})
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
}

func (d *Daemon) backfill(files []watcher.FileInfo) {
	d.backfillAt.Store(d.clock.Now().UnixNano())
	d.filesTotal.Store(int64(len(files)))
	d.obs.Reg.Set(obs.MBackfillFiles, nil, float64(len(files)))
	sorted := append([]watcher.FileInfo(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ModTime.After(sorted[j].ModTime) })
	d.obs.Event("backfill_start", "files", fmt.Sprint(len(files)))
	for _, f := range sorted {
		select {
		case <-d.stop:
			return
		default:
		}
		res := d.ingest(f.Path)
		d.filesCopied.Add(1)
		d.obs.Reg.Set(obs.MCopiedFiles, nil, float64(d.filesCopied.Load()))
		if res.Bytes > 0 && d.cfg.CopyThrottle > 0 {
			d.clock.Sleep(d.cfg.CopyThrottle)
		}
	}
	d.backfillDone.Store(true)
	d.obs.Reg.Set(obs.MCaughtUp, nil, 1)
	d.obs.Event("backfill_done", "files", fmt.Sprint(len(files)))
}

// Start opens the listener and the exporter and starts the watcher.
func (d *Daemon) Start() error {
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	d.srv.Register("status", server.KindQuery, d.opStatus)
	d.srv.Register("coverage", server.KindQuery, d.opCoverage)
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
	d.refreshing.Add(1)
	go d.refreshLoop()
	d.obs.Event("serving", "socket", d.cfg.Socket, "claude_dir", d.cfg.ClaudeDir, "state_dir", d.cfg.StateDir,
		"version", d.opt.Version)
	return nil
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
	return map[string]any{"service": serviceName, "version": d.opt.Version, "protocol_version": wire.ProtocolVersion}
}

// realPath resolves symlinks in p when it exists, else returns p unchanged.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
