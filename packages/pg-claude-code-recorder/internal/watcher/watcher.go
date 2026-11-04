// Package watcher reports changed paths under the Claude directory and the
// control ends directory to a sink. It treats every event as a hint: per event
// it filters the path, does one lstat, and reports. It does not copy, parse or
// know about the store; the serve packet joins it to those.
package watcher

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"
)

const (
	defaultSweepInterval = 15 * time.Minute
	defaultMaxReplayGap  = 10 * time.Minute
)

// Event is one changed path as the watcher saw it after one lstat. Gone is true
// when the path no longer exists; that is a hint only, and a deletion needs a
// full sweep that finds no file with the same identity elsewhere.
type Event struct {
	Path string
	Info FileInfo
	Gone bool
	// Sweep is true for an event a full sweep produced, false for one a
	// filesystem hint produced. A sink MAY skip an unchanged file only for a
	// sweep event: a hint always means something changed.
	Sweep bool
}

// Config configures a Watcher. Sink is required and MUST NOT call back into
// the Watcher (it runs under the watcher's lock, which serializes sink calls).
type Config struct {
	// Roots are the watched directories: the Claude directory and
	// <controlStateDir>/ends, in one stream.
	Roots []string
	// Filter decides, before any lstat, whether a path is interesting.
	// nil accepts everything.
	Filter func(path string) bool
	// DirFilter, when set, runs on every subdirectory a sweep is about to read;
	// false prunes the subtree before any directory read. It MUST accept
	// every directory that can hold a path Filter accepts.
	DirFilter func(dir string) bool
	// SweepDirs are the directories a sweep walks and SweepFiles the single
	// files it lstats directly. Both empty means a sweep walks Roots whole.
	// Roots still names what the event stream watches.
	SweepDirs  []string
	SweepFiles []string
	Sink       func(Event)
	// OnSweep, when set, receives the full filtered file list of every
	// successful sweep (identity for the copier's deletion decision).
	OnSweep func([]FileInfo)
	// SweepTimer, when set, runs every sweep pass through it: step is the whole
	// pass (the directory walk, OnSweep and the per-file events), so the caller
	// can time the walk as well as the callback. It returns step's error.
	SweepTimer func(step func() error) error
	// Checkpoint is optional; nil disables persistence (always sweeps at start).
	Checkpoint    Checkpoint
	SweepInterval time.Duration // default 15m
	MaxReplayGap  time.Duration // default 10m
	Now           func() time.Time
	Logger        *slog.Logger
	// DisableEvents leaves the watcher sweep-only (no FSEvents or inotify
	// stream); the serve unit tests drive it with Wake and Rescan.
	DisableEvents bool

	backend backend // test seam; nil selects the platform backend
}

// Watcher is the FSEvents/inotify watcher with its sweeps and test seams.
type Watcher struct {
	cfg Config
	b   backend

	mu        sync.Mutex // serializes handle, sweep and sink calls
	lastSweep time.Time
	state     State

	qmu    sync.Mutex
	queue  []Hint
	notify chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

// New validates cfg and builds a Watcher; call Start to begin.
func New(cfg Config) (*Watcher, error) {
	if cfg.Sink == nil {
		return nil, errors.New("watcher: Sink is required")
	}
	if len(cfg.Roots) == 0 {
		return nil, errors.New("watcher: at least one root is required")
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = defaultSweepInterval
	}
	if cfg.MaxReplayGap <= 0 {
		cfg.MaxReplayGap = defaultMaxReplayGap
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	b := cfg.backend
	if b == nil && !cfg.DisableEvents {
		b = newBackend()
	}
	return &Watcher{cfg: cfg, b: b, notify: make(chan struct{}, 1)}, nil
}

// Start loads the checkpoint, chooses replay or sweep, starts the event source
// and the loop. A sweep owed at start runs in the loop, after the stream is
// live, so no event is lost between the sweep and the stream.
func (w *Watcher) Start() error {
	var st State
	if w.cfg.Checkpoint != nil {
		loaded, err := w.cfg.Checkpoint.Load()
		if err != nil {
			w.cfg.Logger.Warn("watcher checkpoint load failed; sweeping", "err", err)
		} else {
			st = loaded
		}
	}
	d := Decision{Reason: "no event source"}
	uuid := ""
	if w.b != nil {
		uuid = w.b.VolumeUUID(w.cfg.Roots[0])
		d = Decide(st, uuid, w.cfg.Now(), w.cfg.MaxReplayGap, w.b.CanReplay())
	}
	if d.Gap {
		w.cfg.Logger.Warn("fsevents_gap", "reason", d.Reason,
			"last_event", st.EventTime, "last_sweep", st.LastSweep)
	}
	w.mu.Lock()
	w.state = st
	w.state.VolumeUUID = uuid
	w.lastSweep = st.LastSweep
	w.mu.Unlock()

	if w.b != nil {
		if err := w.b.Start(w.cfg.Roots, d.SinceID, d.Replay, w.hint); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.loop(ctx, !d.Replay)
	return nil
}

// Close stops the event source and the loop.
func (w *Watcher) Close() error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	if w.b != nil {
		w.b.Stop()
	}
	<-w.done
	w.cancel = nil
	return nil
}

// Wake injects an event hint for path (test seam). It runs the same
// filter-lstat-report path as a real event, synchronously.
func (w *Watcher) Wake(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handle(path)
}

// Rescan forces a sweep now (test seam) and returns its error.
func (w *Watcher) Rescan() error { return w.sweep() }

// LastSweep is the time of the last successful sweep (zero if none): the
// heartbeat the serve packet exports as a gauge.
func (w *Watcher) LastSweep() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSweep
}

// hint is called from the backend's thread and never blocks.
func (w *Watcher) hint(h Hint) {
	w.qmu.Lock()
	w.queue = append(w.queue, h)
	w.qmu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *Watcher) loop(ctx context.Context, sweepFirst bool) {
	defer close(w.done)
	tick := time.NewTicker(w.cfg.SweepInterval)
	defer tick.Stop()
	if sweepFirst {
		w.sweepLogged()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.sweepLogged()
		case <-w.notify:
			w.drain()
		}
	}
}

func (w *Watcher) sweepLogged() {
	if err := w.sweep(); err != nil {
		w.cfg.Logger.Warn("watcher sweep failed", "err", err)
	}
}

func (w *Watcher) drain() {
	w.qmu.Lock()
	hints := w.queue
	w.queue = nil
	w.qmu.Unlock()

	mustScan := false
	var maxID uint64
	w.mu.Lock()
	for _, h := range hints {
		if h.EventID > maxID {
			maxID = h.EventID
		}
		if h.MustScan {
			mustScan = true
			continue
		}
		w.handle(h.Path)
	}
	if maxID > w.state.EventID {
		w.state.EventID = maxID
		w.state.EventTime = w.cfg.Now()
		w.saveLocked()
	}
	w.mu.Unlock()
	if mustScan {
		w.sweepLogged()
	}
}

// handle is the per-event path: filter first, then one lstat, then report.
// Callers hold w.mu.
func (w *Watcher) handle(path string) {
	if w.cfg.Filter != nil && !w.cfg.Filter(path) {
		return
	}
	info, regular, err := lstatInfo(path)
	switch {
	case err == nil && regular:
		w.cfg.Sink(Event{Path: path, Info: info})
	case err == nil:
		// A directory or other non-file: nothing to report; new directories
		// are covered by the backend's own scan and by the sweeps.
	case errors.Is(err, fs.ErrNotExist):
		w.cfg.Sink(Event{Path: path, Gone: true})
	default:
		w.cfg.Logger.Warn("watcher lstat failed", "path", path, "err", err)
	}
}

// sweep reports every filtered file under the roots with its identity and, on
// success, records the sweep time. A missing root is skipped (the ends
// directory may not exist yet).
func (w *Watcher) sweep() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cfg.SweepTimer != nil {
		return w.cfg.SweepTimer(w.sweepLocked)
	}
	return w.sweepLocked()
}

// sweepLocked is one sweep pass; the caller holds w.mu.
func (w *Watcher) sweepLocked() error {
	var files []FileInfo
	dirs := w.cfg.SweepDirs
	if len(dirs) == 0 && len(w.cfg.SweepFiles) == 0 {
		dirs = w.cfg.Roots
	}
	for _, root := range dirs {
		infos, err := platformSweep(root, w.cfg.DirFilter)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		for _, fi := range infos {
			if w.cfg.Filter == nil || w.cfg.Filter(fi.Path) {
				files = append(files, fi)
			}
		}
	}
	for _, p := range w.cfg.SweepFiles {
		fi, regular, err := lstatInfo(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if regular && (w.cfg.Filter == nil || w.cfg.Filter(p)) {
			files = append(files, fi)
		}
	}
	now := w.cfg.Now()
	w.lastSweep = now
	w.state.LastSweep = now
	w.saveLocked()
	if w.cfg.OnSweep != nil {
		w.cfg.OnSweep(files)
	}
	for _, fi := range files {
		w.cfg.Sink(Event{Path: fi.Path, Info: fi, Sweep: true})
	}
	return nil
}

func (w *Watcher) saveLocked() {
	if w.cfg.Checkpoint == nil {
		return
	}
	if err := w.cfg.Checkpoint.Save(w.state); err != nil {
		w.cfg.Logger.Warn("watcher checkpoint save failed", "err", err)
	}
}
