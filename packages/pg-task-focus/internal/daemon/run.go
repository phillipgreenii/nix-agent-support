package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

// tracerProvider is the injected provider, else the global one Init installed.
func (d *Daemon) tracerProvider() trace.TracerProvider {
	if d.p.Tracing != nil {
		return d.p.Tracing
	}
	return otel.GetTracerProvider()
}

// Addr is the address the API listens on.
func (d *Daemon) Addr() string { return d.ln.Addr().String() }

// Routes lists "METHOD /path" of every endpoint the daemon serves, for the
// contract test.
func (d *Daemon) Routes() []string { return d.srv.Routes() }

// Engine is the open engine, for tests.
func (d *Daemon) Engine() *engine.Engine { return d.eng }

// Alerter is the alert runner, for tests.
func (d *Daemon) Alerter() *Alerter { return d.alerter }

// Done is closed once the daemon has shut down.
func (d *Daemon) Done() <-chan struct{} { return d.done }

// alertsReading is the scheduler's reading for /healthz.
func (d *Daemon) alertsReading() server.AlertsReading {
	if d.alerter == nil {
		return server.AlertsReading{}
	}
	id, next, ok := d.alerter.Reading()
	if !ok {
		return server.AlertsReading{}
	}
	return server.AlertsReading{RunningCycle: id, NextReminder: next}
}

// supervise wires the engine to the rest of the daemon, runs the background
// loops and shuts everything down when ctx ends. Every callback registered
// with the engine recovers its own panics: callbacks run while the engine
// serializes writes and a panic there would skip the rest of that commit's
// notifications.
func (d *Daemon) wire(ctx context.Context) {
	e := d.eng
	d.srv.Attach(e, d.replay, d.recovery)

	player, notifier := d.p.Player, d.p.Notifier
	if player == nil || notifier == nil {
		sys := notify.System{}
		if player == nil {
			player = sys
		}
		if notifier == nil {
			notifier = sys
		}
	}
	d.alerter = NewAlerter(e, d.clk, player, notifier, d.log, d.metrics)
	d.metrics.RegisterProjection(source{d})

	e.OnCommit(func(v engine.Version) {
		defer d.recoverCallback("OnCommit")
		d.metrics.StateVersion.Set(float64(v.LogLines))
		d.srv.Notify()
		d.alerter.Poll() // the scheduler is poll-driven: poll after every commit
	})
	e.OnHealthChange(func(h engine.Health) {
		defer d.recoverCallback("OnHealthChange")
		d.metrics.SetReadOnly(h.ReadOnly)
		if h.ReadOnly {
			d.log.Error("store is read-only; restart pg-task-focus to recover", "reason", string(h.Reason), "since", h.Since.UTC().Format(time.RFC3339))
		}
		d.srv.Notify()
	})
	d.metrics.StateVersion.Set(float64(e.Version().LogLines))
	if log := e.Snapshot().Model.Log(); len(log) > 0 {
		d.metrics.MarkAppend(log[len(log)-1].At.Time()) // the last append before this process, from the log itself
	}
	d.metrics.SetReadOnly(e.Health().ReadOnly)

	go d.alerter.Run(ctx)
	d.alerter.Poll()

	// The first store writability check gates readiness; later ones feed the
	// gauge and never gate reads.
	d.probe()
}

// supervise runs the periodic store probe until ctx ends, then shuts down.
func (d *Daemon) supervise(ctx context.Context) {
	defer close(d.done)
	interval := d.p.ProbeInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return
		case <-ticker.C:
			d.probe()
		}
	}
}

// probe checks that the data directory can still take a write, and publishes
// it. The first pass makes the server ready.
func (d *Daemon) probe() {
	err := d.eng.Probe()
	ok := err == nil
	d.metrics.SetWritable(ok, d.eng.StoreSize())
	d.metrics.SetReadOnly(d.eng.Health().ReadOnly)
	d.srv.SetWritable(ok)
	if !ok {
		d.log.Warn("store writability check failed", "error", err.Error())
		d.srv.SetFailing("store_writable")
		return
	}
	if d.eng.Health().ReadOnly {
		d.srv.SetFailing("")
	}
	d.markReadyOnce()
}

func (d *Daemon) markReadyOnce() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readied {
		return
	}
	d.readied = true
	d.srv.MarkReady()
	d.log.Info("ready", "addr", d.ln.Addr().String())
}

// recoverCallback is deferred by every engine callback.
func (d *Daemon) recoverCallback(name string) {
	if r := recover(); r != nil {
		d.log.Error("engine callback panicked", "callback", name, "panic", fmt.Sprint(r))
	}
}

// Stop shuts the daemon down: the API drains, the alerter stops, the engine
// closes (releasing the data-directory lock) and telemetry is flushed. It is
// safe to call twice and returns when the shutdown is complete.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
	})
	<-d.done
}

func (d *Daemon) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.log.Info("stopping")
	if err := d.http.Shutdown(ctx); err != nil {
		_ = d.http.Close()
	}
	if err := d.eng.Close(); err != nil {
		d.log.Error("closing the store failed", "error", err.Error())
	}
	d.flush()
}

// flush flushes telemetry; it runs before the process exits, a startup
// failure included, so the Error line is not lost.
func (d *Daemon) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if d.otel != nil {
		_ = d.otel(ctx)
	}
}

// Reload re-reads the configuration file, as SIGHUP does. A bad document keeps
// the previous configuration and reports through /healthz and config_valid; a
// reload that removes the active profile is a bad reload; a changed
// listen_port or public_url is accepted but takes effect only on restart, and
// is reported. It returns the problem when the reload was refused.
func (d *Daemon) Reload() error {
	now := d.clk.Now()
	d.mu.Lock()
	prev := d.cfg
	gen := d.gen + 1
	d.mu.Unlock()

	next, err := loadConfig(d.p.ConfigPath)
	if err == nil {
		err = d.eng.SetConfig(next, gen)
	}
	if err != nil {
		d.metrics.ConfigReloads.WithLabelValues(reloadResult(err)).Inc()
		d.metrics.ConfigValid.Set(0)
		msg := Describe(err)
		d.log.Warn("configuration reload refused; keeping the previous configuration", "problems", msg)
		d.srv.SetConfigState(server.ConfigState{Valid: false, Digest: prev.Digest(), LoadedAt: d.loadedAt(), ReloadError: msg, RestartRequired: nil})
		d.srv.Notify()
		return err
	}
	restart := config.RestartRequired(prev, next)
	d.mu.Lock()
	d.cfg, d.gen = next, gen
	d.mu.Unlock()
	result := "ok"
	if len(restart) > 0 {
		result = "restart_required"
		d.log.Warn("the reload changed settings that take effect only on restart", "settings", strings.Join(restart, ","))
	}
	d.metrics.ConfigReloads.WithLabelValues(result).Inc()
	d.metrics.ConfigValid.Set(1)
	d.metrics.LastReloadOK.Set(float64(now.Unix()))
	d.srv.SetConfigState(server.ConfigState{Valid: true, Digest: next.Digest(), LoadedAt: now, RestartRequired: restart})
	d.setLoadedAt(now)
	d.log.Info("configuration reloaded", "config_digest", next.Digest())
	d.alerter.Poll() // a reload may change a cycle type's sound or interval
	return nil
}

func (d *Daemon) loadedAt() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loaded
}

func (d *Daemon) setLoadedAt(t time.Time) {
	d.mu.Lock()
	d.loaded = t
	d.mu.Unlock()
}

// reloadResult classifies a refused reload for the config_reload_total label.
func reloadResult(err error) string {
	var ve *config.ValidationError
	if errors.As(err, &ve) {
		for _, p := range ve.Problems {
			if strings.HasPrefix(p.Path, "/profiles/") && strings.Contains(p.Message, "active profile") {
				return "refused"
			}
		}
	}
	return "invalid"
}

// Describe summarizes a configuration problem for /healthz without echoing a
// configured value: the number of problems and the JSON pointer of each, never
// the messages, which may quote what was written.
func Describe(err error) string {
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		return "the configuration could not be read"
	}
	paths := make([]string, 0, len(ve.Problems))
	for _, p := range ve.Problems {
		if p.Path == "" {
			paths = append(paths, "(document)")
		} else {
			paths = append(paths, p.Path)
		}
	}
	return fmt.Sprintf("%d problem(s) at: %s; run `pg-task-focus config check` for the details", len(ve.Problems), strings.Join(paths, ", "))
}

// source adapts the daemon to the projection collector.
type source struct{ d *Daemon }

func (s source) Snapshot() (engine.Snapshot, bool) {
	if s.d.eng == nil {
		return engine.Snapshot{}, false
	}
	return s.d.eng.Snapshot(), true
}

func (s source) Now() time.Time { return s.d.clk.Now() }

func (s source) NextReminder() (time.Time, bool) {
	if s.d.alerter == nil {
		return time.Time{}, false
	}
	_, at, ok := s.d.alerter.Reading()
	return at, ok
}

func (s source) StoreHealth() view.StoreHealth {
	h := s.d.eng.Health()
	return view.StoreHealth{ReadOnly: h.ReadOnly, Reason: string(h.Reason), Since: h.Since}
}
