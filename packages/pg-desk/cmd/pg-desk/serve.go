package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/httpapi"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/telemetry"
)

// shutdownGrace bounds how long a SIGINT/SIGTERM shutdown waits for
// in-flight requests to finish (serve only ever reads committed store rows,
// so requests are cheap) before forcing the listener closed.
const shutdownGrace = 5 * time.Second

// defaultServeLogPath is the fallback log path when neither the launchd
// module (packet 10) nor the operator's config sets serve.log, per the
// Binding decisions section: "Default log path (if the launchd module
// doesn't override it): ~/Library/Logs/pg-desk-serve.log (design section
// 7.9)".
const defaultServeLogPathSuffix = "Library/Logs/pg-desk-serve.log"

// serveAddr and servePort are operator overrides for local/manual runs
// (this packet's own Validation section: "start pg-desk serve --port
// <ephemeral> against a fixture store"). Production behavior reads
// serve.addr from Config (Binding decisions: "reads its listen address from
// serve.addr ... and is otherwise soak-port-agnostic ... never hardcoded in
// this package") — these flags exist only as an explicit, operator-supplied
// override of that config value, never a hardcoded default.
var (
	serveAddr string
	servePort string
)

// serveCmd implements `pg-desk serve`: the soak-port-only HTTP server
// (Objective; docs/behavior/pg-desk/serve.md). It reads the store only,
// gates every route behind httpapi.NewHandler's 503-until-first-
// interpretation check, and logs to serve.log (or the default above).
//
// serve is the ONLY pg-desk subcommand that wires telemetry.Init: its
// WARN/ERROR-level operational log lines export over OTLP as
// {service_name="pg-desk-serve"} once OTEL_EXPORTER_OTLP_ENDPOINT is
// configured (darwin/modules/pg-desk-serve's obs.mkEmitterEnv call).
// `run`/`run issue` already log structured JSON to stderr and are
// deliberately untouched by this docket.
//
// SIGHUP config reload (design doc section 7.7: "reloads its config on
// SIGHUP") is NOT wired by this packet: it is untested by this packet's own
// Validation section and its own Acceptance Criteria, and is left as a
// follow-up rather than silently dropped.
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve the triage payload and /metrics (soak port only)",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", "", "listen address (host:port); overrides serve.addr from config")
	serveCmd.Flags().StringVar(&servePort, "port", "", "listen port only, binding 127.0.0.1 (a convenience alias for --addr; mutually exclusive with it)")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	if serveAddr != "" && servePort != "" {
		return fmt.Errorf("serve: --addr and --port are mutually exclusive")
	}

	cfg, err := config.Load(cmd.Context())
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	addr := cfg.Serve.Addr
	switch {
	case serveAddr != "":
		addr = serveAddr
	case servePort != "":
		addr = "127.0.0.1:" + servePort
	}
	if addr == "" {
		return fmt.Errorf("serve: no listen address: set serve.addr in config, or pass --addr/--port")
	}

	logFile, err := openServeLogFile(cfg.Serve.Log)
	if err != nil {
		return fmt.Errorf("serve: open log file: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	// Telemetry (design section 9): best-effort — a missing/unreachable
	// OTLP endpoint installs a no-op LoggerProvider and never blocks
	// startup. Scoped to serve only; run/run issue keep their existing
	// unstructured logging untouched (see serveCmd's own doc comment).
	shutdown, _ := telemetry.Init(cmd.Context(), "pg-desk-serve", Version)
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancelShutdown()
		_ = shutdown(shutdownCtx)
	}()
	logger := slog.New(telemetry.Fanout(slog.NewTextHandler(logFile, nil), telemetry.NewSlogHandler()))

	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return fmt.Errorf("serve: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	handler, err := httpapi.NewHandler(st, cfg)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	srv := &http.Server{Addr: addr, Handler: handler} //nolint:gosec // addr/port are operator-supplied, never network-untrusted input

	// main.go wires SIGINT/SIGTERM into cmd.Context() via
	// signal.NotifyContext, which only CONVERTS the signal into context
	// cancellation — it does not itself terminate the process. serve is a
	// long-lived launchd user agent (design doc section 7.7), so it MUST act
	// on that cancellation itself (a graceful http.Server.Shutdown) rather
	// than relying on the OS default signal action, or launchd's stop
	// request would never actually end the process (design section 7.9:
	// "exit 0 ... on a clean shutdown").
	serveErr := make(chan error, 1)
	logger.Info("pg-desk serve listening", "addr", addr)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve: listen failed", "error", err)
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-cmd.Context().Done():
		logger.Info("pg-desk serve shutting down", "reason", cmd.Context().Err())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("serve: shutdown failed", "error", err)
			return fmt.Errorf("serve: shutdown: %w", err)
		}
		return nil
	}
}

// openServeLogFile opens (creating parent directories as needed) the log
// file at path, or the default path under the user's home directory when
// path is empty.
func openServeLogFile(path string) (*os.File, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir for default log path: %w", err)
		}
		path = filepath.Join(home, defaultServeLogPathSuffix)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
