// Package daemon runs pg-task-focus: it loads the configuration, binds the
// loopback listener, opens the event store (the single writer), serves the
// HTTP API, plays the overtime alerts, reloads the configuration on SIGHUP and
// shuts down cleanly. It is the wiring; the rules live in the engine and the
// handlers in internal/server.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// Params configures Start.
type Params struct {
	// ConfigPath is the configuration file; DataDir the data directory.
	ConfigPath, DataDir string
	// Version is the build version.
	Version string
	// Log is where the JSON log lines go (stdout under launchd); nil means
	// io.Discard.
	Log io.Writer
	// Clock is the time source; nil means the system clock.
	Clock clock.Clock
	// Player and Notifier play and send alerts; nil means the system ones.
	Player   notify.SoundPlayer
	Notifier notify.Notifier
	// Host is the interface to bind; it MUST be a loopback address and
	// defaults to 127.0.0.1. A test passes port 0 through the config and reads
	// the bound address from Addr.
	Host string
	// ProbeInterval is how often the store's writability is probed; zero
	// means 30 seconds. HeartbeatInterval is the /stream heartbeat.
	ProbeInterval, HeartbeatInterval time.Duration
	// WriteTimeout is the http server's write timeout; zero means 30 seconds.
	// /stream clears it per connection; a test shortens it to prove that.
	WriteTimeout time.Duration
	// NewID overrides event id generation, for tests.
	NewID func() event.ID
	// FS is the file system the log goes through; nil means the operating
	// system. A test passes a fault FS.
	FS store.FS
	// OTLPLogs and Tracing, when set, replace the OpenTelemetry setup the
	// daemon makes from the environment: a test injects an in-memory log
	// handler and tracer provider. Production leaves both nil.
	OTLPLogs slog.Handler
	Tracing  trace.TracerProvider
	// ExtraObserver, when set, is also told what the engine does, for tests.
	ExtraObserver engine.Observer
}

// StartupError is a failure to start. The daemon has already logged it at
// Error, with its cause, and flushed.
type StartupError struct {
	Stage string
	Err   error
}

func (e *StartupError) Error() string {
	return "pg-task-focus failed to start (" + e.Stage + "): " + e.Err.Error()
}
func (e *StartupError) Unwrap() error { return e.Err }

// Daemon is a running daemon.
type Daemon struct {
	p       Params
	log     *slog.Logger
	metrics *obs.Metrics
	srv     *server.Server
	http    *http.Server
	ln      net.Listener
	eng     *engine.Engine
	clk     clock.Clock
	otel    obs.ShutdownFunc

	// alerter is built by wire, after the HTTP server is serving, and read by
	// the goroutines that serve /healthz, /readyz and /metrics. It MUST reach
	// them through this pointer and never through a plain field: the atomic
	// store is what makes everything NewAlerter wrote visible to a reader that
	// sees the pointer. It is nil until wire has run.
	alerter atomic.Pointer[Alerter]

	mu       sync.Mutex
	cfg      *config.Config
	gen      int64
	replay   server.ReplayInfo
	recovery wire.HealthRecover
	done     chan struct{}
	cancel   context.CancelFunc
	stopOnce sync.Once
	readied  bool
	loaded   time.Time
}

// Start brings the daemon up and returns once it is serving. The listener is
// bound and /healthz and /metrics answer at once; the API answers
// 503 not_ready until replay has finished and the first store check has
// passed, which happens in the background. A failure to start is logged at
// Error and flushed, then returned as a *StartupError. Stop (or ctx ending)
// shuts it down.
func Start(ctx context.Context, p Params) (*Daemon, error) {
	started := time.Now()
	clk := p.Clock
	if clk == nil {
		clk = clock.Real()
	}
	logw := p.Log
	if logw == nil {
		logw = io.Discard
	}
	d := &Daemon{p: p, clk: clk, done: make(chan struct{})}

	// Telemetry first, so even a startup failure reaches the collector. It is
	// best-effort and loopback-only.
	early := slog.New(slog.NewJSONHandler(logw, nil))
	var otlp slog.Handler
	if p.OTLPLogs != nil || p.Tracing != nil {
		otlp = p.OTLPLogs
	} else {
		d.otel = obs.Init(ctx, "pg-task-focus", p.Version, func(msg string) { early.Warn(msg) })
		otlp = obs.OTLPHandler()
	}
	d.log = obs.NewLogger(logw, otlp)
	d.metrics = obs.New(p.Version, server.SchemaVersion, started)

	fail := func(stage string, err error, attrs ...any) (*Daemon, error) {
		d.log.Error("startup failed", append([]any{"stage", stage, "error", err.Error()}, attrs...)...)
		d.flush()
		return nil, &StartupError{Stage: stage, Err: err}
	}

	cfg, err := loadConfig(p.ConfigPath)
	if err != nil {
		d.metrics.ConfigValid.Set(0)
		return fail("config", err)
	}
	d.cfg = cfg
	d.loaded = started
	d.gen = started.UnixMilli()
	d.metrics.ConfigValid.Set(1)
	d.metrics.LastReloadOK.Set(float64(started.Unix()))
	d.log.Info("starting", "version", p.Version, "config_digest", cfg.Digest(), "listen_port", cfg.ListenPort())

	host := p.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fail("listen", fmt.Errorf("%q is not a loopback address; the API is loopback-only", host))
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(cfg.ListenPort())))
	if err != nil {
		return fail("listen", err)
	}
	d.ln = ln
	port := cfg.ListenPort()
	if port == 0 { // a test binds an ephemeral port
		port = ln.Addr().(*net.TCPAddr).Port
	}

	d.srv = server.New(server.Options{
		Logger: d.log, Metrics: d.metrics, Clock: clk, Version: p.Version, Port: port, PublicURL: cfg.PublicURL(),
		Started: started, HeartbeatInterval: p.HeartbeatInterval, TracerProvider: d.tracerProvider(),
		Alerts: d.alertsReading,
	})
	d.srv.SetConfigState(server.ConfigState{Valid: true, Digest: cfg.Digest(), LoadedAt: started})
	writeTimeout := p.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 30 * time.Second
	}
	d.http = &http.Server{
		Handler: d.srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: writeTimeout, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10,
	}
	go func() {
		if err := d.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.log.Error("http server stopped", "error", err.Error())
		}
	}()

	if err := d.open(); err != nil {
		_ = d.http.Close()
		var se *StartupError
		errors.As(err, &se)
		stage := "store"
		if se != nil {
			stage = se.Stage
		}
		attrs := []any{}
		var ce *store.CorruptError
		if errors.As(err, &ce) {
			attrs = append(attrs, "line", ce.Line)
		}
		return fail(stage, errors.Unwrap(err), attrs...)
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d.cancel = cancel
	d.wire(runCtx)
	go d.supervise(runCtx)
	go func() {
		select {
		case <-ctx.Done():
			d.Stop()
		case <-d.done:
		}
	}()
	return d, nil
}

// loadConfig reads and validates the configuration file.
func loadConfig(path string) (*config.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the configuration: %w", err)
	}
	return config.Parse(raw)
}

// open opens the store (taking the data-directory lock), replays the log and
// applies the startup checks. The error is a *StartupError wrapping the cause.
func (d *Daemon) open() error {
	d.log.Info("replay start", "data_dir", d.p.DataDir)
	opened := time.Now()
	obsv := d.compose()
	eng, err := engine.Open(engine.Options{
		Dir: d.p.DataDir, FS: d.p.FS, Config: d.cfg, ConfigGeneration: d.gen, Clock: d.clk, NewID: d.p.NewID, Observer: obsv,
	})
	if err != nil {
		stage := "store"
		if errors.Is(err, store.ErrLocked) {
			stage = "lock"
		}
		return &StartupError{Stage: stage, Err: err}
	}
	if err := CheckActiveProfile(d.cfg, eng.Snapshot().Model.Profile()); err != nil {
		_ = eng.Close()
		return &StartupError{Stage: "active_profile", Err: err}
	}
	d.eng = eng
	d.replaySpans(opened)
	return nil
}

// replaySpans records the startup replay as a trace: a replay span, with a
// quarantine child when recovery trimmed the end of the log.
func (d *Daemon) replaySpans(opened time.Time) {
	tracer := d.tracerProvider().Tracer(obs.ScopeName)
	d.mu.Lock()
	replay, rec := d.replay, d.recovery
	d.mu.Unlock()
	ctx, span := tracer.Start(context.Background(), "pg-task-focus.replay", trace.WithTimestamp(opened))
	obs.Attrs(span, attribute.Int("events", replay.Events))
	if rec.TornTail || rec.UncommittedBatches > 0 {
		_, q := tracer.Start(ctx, "pg-task-focus.replay.quarantine", trace.WithTimestamp(opened))
		q.End(trace.WithTimestamp(opened))
	}
	span.End(trace.WithTimestamp(opened.Add(replay.Duration)))
}

// CheckActiveProfile refuses a log whose active profile the configuration no
// longer defines. It is the startup counterpart of the reload check, which
// already refuses to drop the active profile: the active profile is always
// one the configuration defines, at start and at every reload, and a state
// that rule forbids is not entered through a restart. An empty profile (a log
// with no profile yet) passes.
//
// This is a decision, not a library rule (the engine opens such a log): see
// the ADR amendment for pg-task-focus's daemon, "The log's active profile is
// checked at start". The restart-only recovery fits it: the operator restores
// the profile in the configuration and restarts, as for read-only mode.
func CheckActiveProfile(cfg *config.Config, active string) error {
	if active == "" {
		return nil
	}
	if _, ok := cfg.Profile(active); ok {
		return nil
	}
	var names []string
	for _, p := range cfg.Profiles() {
		names = append(names, p.Name)
	}
	return fmt.Errorf("the log's active profile %q is not defined in the configuration (it defines: %s); "+
		"restore profile %q in the configuration and restart, or, to move off it, start with a configuration that still defines it and run `pg-task-focus profile change`",
		active, strings.Join(names, ", "), active)
}
