// Package sync — daemon mode.
//
// `pg-pr sync --daemon` runs a fingerprint-driven loop: every interval it polls
// GitHub for a cheap per-PR change signature (the detector, fingerprintTick),
// diffs it against the previous tick plus the open merge-request beads, and
// enqueues only the changed PRs onto two dedup FIFO queues (mine / team). Two
// worker goroutines drain the queues serially via refreshPR — the only path
// that mutates beads or the snapshot — and a single snapshot-owner goroutine
// rebuilds and publishes the dashboard snapshot per PR. The daemon owns:
//
//   - A file lock at $XDG_RUNTIME_DIR/pg-pr/daemon.lock (or os.TempDir
//     fallback) to enforce a single instance per user.
//   - A SIGHUP listener that re-reads the config file and atomically replaces
//     the engine's config without restarting the process.
//   - The caller-provided context, which is cancelled by SIGINT/SIGTERM at the
//     CLI layer; cancellation stops the tick loop, drains the workers, and
//     exits cleanly.
//
// A failed fingerprint poll is logged and skipped (no beads change); the next
// tick retries.
package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	stdsync "sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/httpapi"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
)

// DefaultMetricsAddr is the address the daemon's Prometheus scrape
// endpoint binds to when DaemonOpts.MetricsAddr is unset. Chosen out of
// the user range (9818) to leave 9100, 9200, and the common otelcol
// ports free for other tooling.
const DefaultMetricsAddr = "127.0.0.1:9818"

// DefaultDaemonInterval is used when DaemonOpts.Interval is unset/zero.
const DefaultDaemonInterval = 60 * time.Second

// DaemonOpts configures Engine.Daemon. Zero values are accepted: Interval
// defaults to DefaultDaemonInterval, Logger to slog.Default, LockDir to
// $XDG_RUNTIME_DIR/pg-pr (with os.TempDir fallback), and the Sighup channel
// to a private listener registered for SIGHUP.
type DaemonOpts struct {
	// Interval between sync iterations. <=0 means DefaultDaemonInterval.
	Interval time.Duration

	// LockDir overrides the default lock directory. Tests inject a temp dir.
	LockDir string

	// Sighup overrides the SIGHUP source. Tests inject a synthetic channel;
	// nil means register a real SIGHUP listener.
	Sighup <-chan os.Signal

	// Logger is the structured logger; nil means slog.Default.
	Logger *slog.Logger

	// ReloadConfig returns a fresh config on SIGHUP. nil means config.Load.
	// Tests inject a stub.
	ReloadConfig func(context.Context) (*config.Config, error)

	// MetricsAddr is the bind address for the Prometheus scrape endpoint
	// (`/metrics`). An empty string disables the endpoint; the sentinel
	// "default" (or any unset zero value when MetricsEnabled is true) maps
	// to DefaultMetricsAddr. Callers wanting the default explicitly should
	// pass DefaultMetricsAddr.
	//
	// The address is interpreted as a `net.Listen` "tcp" address; tests
	// can pass "127.0.0.1:0" to bind a random port and read the actual
	// port via the MetricsListener callback.
	MetricsAddr string

	// MetricsListener, when non-nil, is invoked with the bound
	// net.Listener after the metrics HTTP server starts. Tests use this
	// to discover the random port assigned by "127.0.0.1:0".
	MetricsListener func(net.Listener)

	// Dashboard, when non-nil, mounts /api/v1/dashboard on the same listener
	// as /metrics, serving snapshots from this Store. Nil disables the
	// endpoint (back-compat for callers that don't enable the dashboard).
	Dashboard *snapshot.Store
}

// Daemon runs the sync engine in a loop until ctx is cancelled. Returns nil
// on clean shutdown; returns an error only when the daemon cannot start
// (lock already held, lock dir creation failed, etc.).
func (e *Engine) Daemon(ctx context.Context, opts DaemonOpts) error {
	if opts.Interval <= 0 {
		opts.Interval = DefaultDaemonInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.LockDir == "" {
		opts.LockDir = filepath.Join(xdgRuntimeDir(), "pg-pr")
	}
	if opts.ReloadConfig == nil {
		opts.ReloadConfig = config.Load
	}
	if err := os.MkdirAll(opts.LockDir, 0o700); err != nil {
		return fmt.Errorf("daemon: create lock dir %s: %w", opts.LockDir, err)
	}
	// Wire the snapshot store + interval onto the engine so each Sync
	// iteration populates the dashboard. When Dashboard is nil this is a
	// no-op (the engine's snapshot-building branch checks for nil).
	if opts.Dashboard != nil {
		e.SetDashboardStore(opts.Dashboard, opts.Interval)
	}
	lockPath := filepath.Join(opts.LockDir, "daemon.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("daemon: open lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("daemon: another pg-pr daemon is holding the lock at %s", lockPath)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	sighup := opts.Sighup
	if sighup == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP)
		defer signal.Stop(ch)
		sighup = ch
	}

	// Start the Prometheus /metrics endpoint. Empty MetricsAddr disables it.
	metricsShutdown, err := startMetricsServer(ctx, opts)
	if err != nil {
		return fmt.Errorf("daemon: start metrics server: %w", err)
	}
	defer metricsShutdown()

	opts.Logger.Info("pg-pr daemon starting",
		"interval", opts.Interval.String(),
		"lock", lockPath,
		"metrics_addr", opts.MetricsAddr)
	defer opts.Logger.Info("pg-pr daemon stopped")

	// Detector state lives on the engine; only this goroutine touches it.
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}

	// Workers always have a place to send snapshot deltas. When the dashboard
	// is disabled this is a throwaway sink that's simply never served.
	ownerStore := opts.Dashboard
	if ownerStore == nil {
		ownerStore = snapshot.NewStore()
	}

	updates := make(chan snapshotUpdate, 64)
	ownerDone := make(chan struct{})
	go func() {
		e.runSnapshotOwner(updates, ownerStore)
		ownerDone <- struct{}{}
	}()

	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()
	var wg stdsync.WaitGroup
	wg.Add(2)
	go e.runWorker(ctx, mineQ, "mine", updates, opts.Logger, &wg)
	go e.runWorker(ctx, teamQ, "team", updates, opts.Logger, &wg)

	for ctx.Err() == nil {
		e.fingerprintTick(ctx, mineQ, teamQ, opts.Logger)

		select {
		case <-ctx.Done():
			opts.Logger.Info("pg-pr daemon shutting down", "reason", ctx.Err().Error())
		case <-sighup:
			opts.Logger.Info("SIGHUP received; reloading config")
			cfg, err := opts.ReloadConfig(ctx)
			if err != nil {
				opts.Logger.Error("config reload failed; keeping previous", "err", err.Error())
			} else {
				e.ReplaceCfg(cfg)
				opts.Logger.Info("config reloaded", "path", cfg.Path)
			}
		case <-time.After(opts.Interval):
		}
		if ctx.Err() != nil {
			break
		}
	}

	// Shutdown: workers drain in-flight refreshes then exit on ctx.Done; once
	// both are done, close the update channel so the owner drains and returns.
	wg.Wait()
	close(updates)
	<-ownerDone
	return nil
}

// runWorker drains q serially, calling refreshPR and forwarding the snapshot
// delta to the owner. Exits when the queue is empty AND ctx is cancelled.
func (e *Engine) runWorker(ctx context.Context, q *refreshQueue, group string, updates chan<- snapshotUpdate, log *slog.Logger, wg *stdsync.WaitGroup) {
	defer wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		k, ok := q.dequeue()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
				continue
			}
		}
		telemetry.RefreshQueueDepth.WithLabelValues(group).Set(float64(q.depth()))
		start := e.deps.Now()
		in, err := e.refreshPR(ctx, k.Repo, k.Number)
		telemetry.SyncPRDuration.WithLabelValues(k.Repo, group).Observe(e.deps.Now().Sub(start).Seconds())
		if err != nil {
			log.Warn("refresh failed", "group", group, "repo", k.Repo, "number", k.Number, "err", err.Error())
			continue
		}
		updates <- snapshotUpdate{Key: k, Input: in} // in may be nil → owner deletes
	}
}

// logSyncOutcome logs the result of one sync iteration.
func logSyncOutcome(log *slog.Logger, sum *Summary, err error, dur time.Duration) {
	if err != nil {
		log.Error("sync iteration failed",
			"err", err.Error(),
			"duration_ms", dur.Milliseconds(),
		)
		return
	}
	if sum == nil {
		log.Info("sync iteration finished", "duration_ms", dur.Milliseconds())
		return
	}
	attrs := []any{
		"duration_ms", dur.Milliseconds(),
		"total_prs", sum.TotalPRs,
		"created", sum.BeadsCreated,
		"updated", sum.BeadsUpdated,
		"closed", sum.BeadsClosed,
		"errors", len(sum.Errors),
	}
	if len(sum.Errors) > 0 {
		// Surface the actual error messages, not just the count. The per-repo
		// state file is overwritten every sync, so a bare count hides the
		// cause (e.g. "invalid issue type: feedback" went unnoticed for a long
		// time precisely because only the count reached the logs). WARN level
		// so it's visible without terminating the daemon.
		details := make([]string, len(sum.Errors))
		for i, se := range sum.Errors {
			details[i] = se.Repo + ": " + se.Message
		}
		log.Warn("sync iteration finished with errors",
			append(attrs, "error_details", details)...)
		return
	}
	log.Info("sync iteration finished", attrs...)
}

// ReplaceCfg swaps the engine's config in-place. Used by Daemon on SIGHUP.
// Safe to call only from the daemon loop (single goroutine).
func (e *Engine) ReplaceCfg(cfg *config.Config) {
	if cfg == nil {
		return
	}
	e.cfgP.Store(cfg)
}

// xdgRuntimeDir returns $XDG_RUNTIME_DIR or os.TempDir() if unset.
func xdgRuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return v
	}
	return os.TempDir()
}

// newStderrHandler builds the daemon's base slog handler at Info level.
// jsonFormat selects JSON vs human-readable text. The writer is injectable
// for tests; production callers use NewJSONHandler/NewTextHandler (os.Stderr).
func newStderrHandler(w io.Writer, jsonFormat bool) slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if jsonFormat {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// NewJSONHandler returns the stderr JSON slog.Handler used by the daemon when
// --log-json is set. CLI wiring composes it with the OTLP bridge handler.
func NewJSONHandler() slog.Handler { return newStderrHandler(os.Stderr, true) }

// NewTextHandler returns the stderr text slog.Handler (default daemon format).
func NewTextHandler() slog.Handler { return newStderrHandler(os.Stderr, false) }

// NewJSONLogger returns a slog.Logger writing structured JSON to stderr.
// Retained for back-compat / standalone use; the daemon path composes
// handlers directly (see cmd/pg-pr/sync.go).
func NewJSONLogger() *slog.Logger { return slog.New(NewJSONHandler()) }

// NewTextLogger returns a slog.Logger writing human-readable text to stderr.
func NewTextLogger() *slog.Logger { return slog.New(NewTextHandler()) }

// startMetricsServer launches the Prometheus scrape endpoint in a
// background goroutine and returns a shutdown closure. When
// opts.MetricsAddr is empty the function returns a no-op shutdown
// without binding to any port — useful for one-shot tests.
//
// A bind failure (port already in use) is fatal because metrics is a
// daemon contract; running without it would silently produce empty
// dashboards. The bound listener is offered to opts.MetricsListener for
// tests using "127.0.0.1:0".
func startMetricsServer(_ context.Context, opts DaemonOpts) (func(), error) {
	if opts.MetricsAddr == "" {
		return func() {}, nil
	}
	ln, err := net.Listen("tcp", opts.MetricsAddr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", opts.MetricsAddr, err)
	}
	if opts.MetricsListener != nil {
		opts.MetricsListener(ln)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", telemetry.MetricsHandler())
	if opts.Dashboard != nil {
		mux.Handle("/api/v1/dashboard", httpapi.DashboardHandler(opts.Dashboard))
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			opts.Logger.Error("metrics endpoint failed", "err", err.Error())
		}
	}()
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}, nil
}
