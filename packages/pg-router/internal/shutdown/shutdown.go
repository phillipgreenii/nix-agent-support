// Package shutdown bounds the daemon's own shutdown. Tracker logs the
// shutdown_started, shutdown_phase and shutdown_done events; Watch arms a hard
// deadline that, once the root context ends, logs shutdown_overrun naming the
// phase in progress and exits non-zero, so a stuck step can never hold the
// pidfile lock past launchd's ExitTimeOut.
//
// Logging goes through a raw slog logger (stderr), never the OTLP emitter: the
// emitter's flush is one of the steps that can hang.
package shutdown

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Deadline is the default for how long a shutdown may take before the watchdog
// hard-exits: the launchd ExitTimeOut (60 s, launchd's silent clamp) minus a 7 s
// margin. cmd/pg-router passes its own, derived from its drain phases.
const Deadline = 53 * time.Second

// ExitCode is the process exit status of a hard exit.
const ExitCode = 10

// Log events.
const (
	EvStarted = "shutdown_started"
	EvPhase   = "shutdown_phase"
	EvDone    = "shutdown_done"
	EvOverrun = "shutdown_overrun"
)

// Tracker records the shutdown step in progress. A nil Tracker is a no-op.
type Tracker struct {
	log *slog.Logger

	mu      sync.Mutex
	began   time.Time
	name    string
	started time.Time
}

// NewTracker returns a Tracker that logs to log (nil logs nothing).
func NewTracker(log *slog.Logger) *Tracker { return &Tracker{log: log} }

// Begin logs shutdown_started and enters the "signal" phase.
func (t *Tracker) Begin() {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	// Begin runs from the context's AfterFunc goroutine, so the caller's own
	// first Phase can already have run; keep that phase rather than reset it.
	if t.began.IsZero() {
		t.began, t.name, t.started = now, "signal", now
	}
	t.mu.Unlock()
	t.emit(EvStarted)
}

// Phase ends the current phase (logging its duration) and starts name.
func (t *Tracker) Phase(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	prev, began := t.name, t.started
	t.name, t.started = name, now
	if t.began.IsZero() {
		t.began = now // Phase ahead of Begin: total runs from here
	}
	t.mu.Unlock()
	if prev != "" {
		t.emit(EvPhase, "phase", prev, "duration_ms", time.Since(began).Milliseconds())
	}
}

// Done logs the last phase and shutdown_done.
func (t *Tracker) Done() {
	if t == nil {
		return
	}
	t.Phase("done")
	t.mu.Lock()
	total := time.Since(t.began)
	t.mu.Unlock()
	t.emit(EvDone, "duration_ms", total.Milliseconds())
}

// Current returns the phase in progress and how long it has run.
func (t *Tracker) Current() (string, time.Duration) {
	if t == nil {
		return "unknown", 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.name == "" {
		return "not_started", 0
	}
	return t.name, time.Since(t.started)
}

func (t *Tracker) emit(ev string, args ...any) {
	if t.log != nil {
		t.log.Info(ev, args...)
	}
}

// Options configures Watch.
type Options struct {
	Deadline time.Duration // zero selects Deadline
	Log      *slog.Logger  // raw logger; nil logs nothing
	Tracker  *Tracker      // names the phase in the overrun line; may be nil
	Exit     func(code int)
}

// Watchdog is an armed deadline.
type Watchdog struct {
	once  sync.Once
	mu    sync.Mutex
	timer *time.Timer
	stopC func() bool
	opt   Options
	fired atomic.Bool
}

// Watch arms the watchdog on ctx: when ctx ends it logs shutdown_started and
// starts the deadline timer. Disarm and the expiry are mutually exclusive.
func Watch(ctx context.Context, opt Options) *Watchdog {
	if opt.Deadline <= 0 {
		opt.Deadline = Deadline
	}
	w := &Watchdog{opt: opt}
	w.stopC = context.AfterFunc(ctx, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		opt.Tracker.Begin()
		w.timer = time.AfterFunc(opt.Deadline, w.expire)
	})
	return w
}

// Disarm stops the watchdog and reports whether it won the race: false means
// the deadline already fired (and the process is exiting).
func (w *Watchdog) Disarm() bool {
	won := false
	w.once.Do(func() { won = true })
	if !won {
		return false
	}
	w.stopC()
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
	return true
}

// Fired reports whether the deadline expired.
func (w *Watchdog) Fired() bool { return w.fired.Load() }

func (w *Watchdog) expire() {
	w.once.Do(func() {
		w.fired.Store(true)
		o := w.opt
		name, elapsed := o.Tracker.Current()
		if o.Log != nil {
			o.Log.Error(EvOverrun,
				"deadline_ms", o.Deadline.Milliseconds(),
				"phase", name,
				"phase_elapsed_ms", elapsed.Milliseconds())
		}
		exit := o.Exit
		if exit == nil {
			exit = osExit
		}
		exit(ExitCode)
	})
}
