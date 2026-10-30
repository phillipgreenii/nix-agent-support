package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// logLines turns pprofserver's one-line reports into log events.
type logLines struct{ log *slog.Logger }

func (l logLines) Write(p []byte) (int, error) {
	l.log.Info("pprof", "message", strings.TrimSpace(string(p)))
	return len(p), nil
}

func (a *app) serve(args []string) int {
	fs := newFlagSet("serve", a.errw)
	var configPath, metricsAddr string
	var pprofOn bool
	fs.StringVar(&configPath, "config", "", "the TOML config file")
	fs.StringVar(&metricsAddr, "metrics-addr", "", "loopback host:port for the Prometheus /metrics endpoint (overrides the config's metrics.addr)")
	fs.BoolVar(&pprofOn, "pprof", false, "open the opt-in pprof endpoint on $XDG_STATE_HOME/pg-claude-code-recorder/pprof.sock")
	if err := fs.Parse(args); err != nil {
		return a.emitError(false, wire.ErrUsage, err.Error())
	}
	path, must := configPath, true
	if path == "" {
		path, must = config.DefaultPath(a.getenv), false
	}
	cfg, err := config.Load(path, must, a.getenv)
	if err != nil {
		return a.emitError(false, wire.ErrUsage, err.Error())
	}
	var logw io.Writer = a.errw
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return a.emitError(false, wire.ErrUnavailable, "open log file: "+err.Error())
		}
		defer func() { _ = f.Close() }()
		logw = f
	}
	log := serve.NewLogger(logw)
	stopPprof := startPprof(pprofOn, a.getenv, logLines{log})
	defer stopPprof()
	d, err := serve.Open(serve.Options{Config: cfg, Logger: log, Version: Version, MetricsAddr: metricsAddr})
	if err != nil {
		log.Error("open_failed", "err", err.Error())
		return a.emitError(false, wire.ErrUnavailable, fmt.Sprintf("cannot start: %v", err))
	}
	if err := d.Run(a.ctx); err != nil {
		log.Error("serve_failed", "err", err.Error())
		return a.emitError(false, wire.ErrUnavailable, fmt.Sprintf("serve: %v", err))
	}
	return wire.ExitOK
}
