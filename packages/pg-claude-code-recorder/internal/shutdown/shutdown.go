// Package shutdown is the recorder's hard deadline on its own shutdown: once
// the root context ends, Watch gives the daemon Deadline to finish, and past it
// logs what was still running and exits with ExitCode, so a goroutine stuck on a
// lock can never hold the state lock (and the next start) hostage.
package shutdown

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Deadline is how long a shutdown may take before the watchdog hard-exits. It
// is a constant, not a config key: the launchd ExitTimeOut is derived from it
// (8 s plus a 7 s margin).
const Deadline = 8 * time.Second

// ExitCode is the process exit status of a hard exit. The launcher turns only
// exit 20 into success, so exit 10 restarts the job.
const ExitCode = wire.ExitShutdownOverrun

// EvOverrun is the log event of a hard exit.
const EvOverrun = "shutdown_overrun"

// Flight is a goroutine that was still running at the deadline.
type Flight struct {
	Name string
	Age  time.Duration
}

// PhaseInfo is the shutdown step in progress when the deadline passed. Attrs
// are extra key/value pairs for the overrun line (the sweep counters).
type PhaseInfo struct {
	Name    string
	Elapsed time.Duration
	Attrs   []any
}

// Hooks are late-bound providers: the watchdog is armed before the daemon
// exists, and Open registers the real ones. Reads are lock-free.
type Hooks struct {
	inFlight atomic.Pointer[func() []Flight]
	phase    atomic.Pointer[func() PhaseInfo]
}

// SetInFlight registers the in-flight provider.
func (h *Hooks) SetInFlight(f func() []Flight) { h.inFlight.Store(&f) }

// SetPhase registers the phase provider.
func (h *Hooks) SetPhase(f func() PhaseInfo) { h.phase.Store(&f) }

// InFlight returns the registered loops, or nil before registration.
func (h *Hooks) InFlight() []Flight {
	if f := h.inFlight.Load(); f != nil {
		return (*f)()
	}
	return nil
}

// Phase returns the registered phase, or "not_started" before registration.
func (h *Hooks) Phase() PhaseInfo {
	if f := h.phase.Load(); f != nil {
		return (*f)()
	}
	return PhaseInfo{Name: "not_started"}
}

// Options configures Watch.
type Options struct {
	Deadline time.Duration // zero selects Deadline
	// Log is the raw, unbuffered logger. It MUST NOT be the daemon's Obs, whose
	// Event takes a mutex a stuck goroutine could hold.
	Log      *slog.Logger
	InFlight func() []Flight
	Phase    func() PhaseInfo
	Exit     func(code int) // nil selects os.Exit
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

// Watch arms the watchdog on ctx: when ctx ends it starts the deadline timer.
// Disarm (a finishing Close) and the expiry are mutually exclusive.
func Watch(ctx context.Context, opt Options) *Watchdog {
	if opt.Deadline <= 0 {
		opt.Deadline = Deadline
	}
	if opt.Exit == nil {
		opt.Exit = osExit
	}
	w := &Watchdog{opt: opt}
	w.stopC = context.AfterFunc(ctx, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
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
		var flights []Flight
		if o.InFlight != nil {
			flights = o.InFlight()
		}
		ph := PhaseInfo{Name: "unknown"}
		if o.Phase != nil {
			ph = o.Phase()
		}
		sort.SliceStable(flights, func(i, j int) bool { return flights[i].Age > flights[j].Age })
		pairs := make([]string, 0, len(flights))
		for _, f := range flights {
			pairs = append(pairs, fmt.Sprintf("%s:%d", f.Name, f.Age.Milliseconds()))
		}
		args := []any{
			"deadline_ms", o.Deadline.Milliseconds(),
			"phase", ph.Name,
			"phase_elapsed_ms", ph.Elapsed.Milliseconds(),
			"in_flight", strings.Join(pairs, ","),
		}
		args = append(args, ph.Attrs...)
		if o.Log != nil {
			o.Log.Error(EvOverrun, args...)
		}
		o.Exit(ExitCode)
	})
}
