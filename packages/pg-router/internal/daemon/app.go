package daemon

import (
	"context"
	"log/slog"
	"time"
)

// Step is one named entry of an App's declared shutdown sequence. Phase, when
// non-empty, is reported through App.SetPhase before Run executes, so a
// shutdown watchdog can say which step was in flight.
type Step struct {
	Phase string
	Run   func()
}

// App is the long-running router's tick loop and shutdown sequence, built by
// constructor injection: every collaborator arrives as a field of AppConfig,
// so a test or a later signal edits one injection point instead of a long
// function. Run owns the loop; the caller keeps argument routing and flags.
type App struct {
	cfg AppConfig
}

// AppConfig holds the injected collaborators of an App.
type AppConfig struct {
	// Interval is the producer tick period; a non-positive value selects 10s.
	Interval time.Duration
	// Startup runs once, before the first tick (the initial dispatch pass).
	Startup func(ctx context.Context)
	// Tick runs one producer/dispatch pass and returns the gate state to hand
	// to the next pass.
	Tick func(ctx context.Context, prevGates string) string
	// Drain runs when ctx ends, before the shutdown steps: it waits for
	// in-flight dispatches and cancels the dispatch context.
	Drain func(ctx context.Context)
	// Shutdown is the ordered list run after Drain; the first entry runs first.
	Shutdown []Step
	// SetPhase receives each phase name ("drain" and every Step.Phase).
	SetPhase func(string)
}

// NewApp builds an App from its collaborators.
func NewApp(cfg AppConfig) *App {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	return &App{cfg: cfg}
}

func (a *App) setPhase(p string) {
	if a.cfg.SetPhase != nil {
		a.cfg.SetPhase(p)
	}
}

// Run ticks until ctx ends, then drains and runs the shutdown sequence in
// declared order.
func (a *App) Run(ctx context.Context) {
	if a.cfg.Startup != nil {
		a.cfg.Startup(ctx)
	}
	a.loop(ctx)
	for _, s := range a.cfg.Shutdown {
		if s.Phase != "" {
			a.setPhase(s.Phase)
		}
		if s.Run != nil {
			s.Run()
		}
	}
}

func (a *App) loop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()
	prevGates := ""
	for {
		prevGates = a.cfg.Tick(ctx, prevGates)
		select {
		case <-ctx.Done():
			slog.Info("run: shutdown requested")
			a.setPhase("drain")
			// Drain BEFORE the shutdown steps: the pre_shutdown sweep must not
			// run while a dispatch is still in flight.
			if a.cfg.Drain != nil {
				a.cfg.Drain(ctx)
			}
			return
		case <-ticker.C:
		}
	}
}
