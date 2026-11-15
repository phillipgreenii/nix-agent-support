package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/x/lifecycle"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// logLines turns pprofserver's one-line reports into log events.
type logLines struct{ log *slog.Logger }

func (l logLines) Write(p []byte) (int, error) {
	l.log.Info("pprof", "message", strings.TrimSpace(string(p)))
	return len(p), nil
}

// defaultShutdownDeadline is the shutdown deadline when --shutdown-deadline is
// not given (the nix module always passes one).
const defaultShutdownDeadline = 8 * time.Second

func (a *app) serve(args []string) int {
	fs := newFlagSet("serve", a.errw)
	var configPath, metricsAddr string
	var pprofOn, rebuild bool
	fs.StringVar(&configPath, "config", "", "the TOML config file")
	fs.StringVar(&metricsAddr, "metrics-addr", "", "loopback host:port for the Prometheus /metrics endpoint (overrides the config's metrics.addr)")
	fs.BoolVar(&pprofOn, "pprof", false, "open the opt-in pprof endpoint on $XDG_STATE_HOME/pg-claude-code-recorder/pprof.sock")
	fs.BoolVar(&rebuild, "rebuild", false, "rebuild index.db from the journal before serving (the way past a newer schema_version)")
	// The shutdown deadline is owned by the deployment: the nix module passes it
	// here and derives launchd's ExitTimeOut from the same number.
	deadline := lifecycle.DeadlineFlag(fs, "shutdown-deadline", defaultShutdownDeadline)
	if err := fs.Parse(args); err != nil {
		return a.emitError(false, wire.ErrUsage, err.Error())
	}
	if *deadline <= 0 {
		return a.emitError(false, wire.ErrUsage, "--shutdown-deadline must be positive")
	}
	path, must := configPath, true
	if path == "" {
		path, must = config.DefaultPath(a.getenv), false
	}
	cfg, err := config.Load(path, must, a.getenv)
	if err != nil {
		return a.emitError(false, wire.ErrBadConfig, err.Error())
	}
	logw := io.Writer(a.errw)
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return a.emitError(false, wire.ErrUnavailable, "open log file: "+err.Error())
		}
		defer func() { _ = f.Close() }()
		logw = f
	}
	log := serve.NewLogger(logw)
	for _, w := range cfg.Warnings {
		log.Warn("config_warning", "msg", w)
	}
	stopPprof := startPprof(pprofOn, a.getenv, logLines{log})
	defer stopPprof()
	d, err := serve.Open(serve.Options{Config: cfg, Logger: log, Version: Version, MetricsAddr: metricsAddr, Rebuild: rebuild})
	if err != nil {
		var older *store.SchemaOlderError
		if errors.As(err, &older) {
			// Refuse rather than rebuild: a rebuild runs for hours with the daemon
			// down, so the operator starts it deliberately (see the README).
			log.Error("schema_rebuild_required", "found", older.Found, "known", older.Known,
				"command", "pg-claude-code-recorder rebuild", "err", err.Error())
			return a.emitError(false, wire.ErrSchemaRebuildRequired, err.Error())
		}
		log.Error("open_failed", "err", err.Error())
		return a.emitError(false, wire.ErrUnavailable, fmt.Sprintf("cannot start: %v", err))
	}
	// The run group owns the signals (the second one takes the default action)
	// from here on; a signal while Open runs takes the default action, as nothing
	// is served yet. It is created after Open so a refused start logs no shutdown.
	// and the hard deadline on the whole shutdown. Its log is the raw logger: a
	// stuck goroutine could hold the lock behind the daemon's own Obs.
	var daemon atomic.Pointer[serve.Daemon]
	grp := lifecycle.New(a.ctx, lifecycle.Options{
		Deadline: *deadline,
		ExitCode: wire.ExitShutdownOverrun,
		Log:      log,
		OnEvent: func(ev lifecycle.Event) {
			// The sweep counters ride with the overrun, before the hard exit.
			if d := daemon.Load(); ev.Name == lifecycle.EvOverrun && d != nil {
				log.Error("shutdown_overrun_counters", d.ShutdownCounters()...)
			}
		},
	})
	d.SetGroup(grp)
	daemon.Store(d)
	grp.Go("daemon", d.Run)
	if err := grp.Run(); err != nil {
		log.Error("serve_failed", "err", err.Error())
		return a.emitError(false, wire.ErrUnavailable, fmt.Sprintf("serve: %v", err))
	}
	return wire.ExitOK
}
