// osx-bridge-api is the shared macOS integration daemon (design: "New
// standalone daemon package: owns all macOS TCC-gated OS integration...
// deployed as a launchd user agent so it has its own clean TCC identity").
//
// It is deployed exclusively as a
// phillipgreenii.system.launchdServices.userAgents LaunchAgent
// (darwin/modules/osx-bridge-api/default.nix) — never invoked directly by a
// human or run inside an interactive terminal session, since the whole
// point of a launchd-owned process is that it is its OWN TCC "responsible"
// identity, independent of whatever hosts an interactive shell (design:
// "Why a shared daemon, not per-backend direct access").
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/osx-bridge-api/internal/calendarapi"
	"github.com/phillipgreenii/osx-bridge-api/internal/eventkitprovider"
	"github.com/phillipgreenii/osx-bridge-api/internal/socketserver"
)

// Version is stamped at build time via mkGoApp's default ldflags target
// (`-X main.Version=<baseVersion>-<srcDigest>`, this repo's ADR 0006).
var Version = "dev"

// unavailableProvider stands in for the calendar Provider when
// eventkitprovider.New failed at startup (TCC denial, or a non-darwin
// build), so the daemon still comes up and answers every calendar op with
// a well-formed wire.ErrUnavailable/wire.ErrUnauthenticated response
// rather than crashing on a nil provider or refusing to start at all.
type unavailableProvider struct{ err error }

func (u unavailableProvider) Calendars() ([]calendarapi.Calendar, error) { return nil, u.err }
func (u unavailableProvider) Events(calendarapi.EventsQuery) ([]calendarapi.Event, error) {
	return nil, u.err
}

// socketEnvVar names the environment variable the LaunchAgent's plist
// script sets to the daemon's socket path, mirroring pg-router's own
// PG_ROUTER_* / pa-monitor's own PA_MONITOR_* env-var convention for
// daemon configuration threaded in from the darwin module rather than a
// CLI flag (a launchd plist's ProgramArguments are far more awkward to
// template than an `export` line in its Script).
const socketEnvVar = "OSX_BRIDGE_API_SOCKET"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv))
}

func run(args []string, getenv func(string) string) int {
	for _, a := range args {
		if a == "--version" || a == "-version" {
			fmt.Println("osx-bridge-api " + Version)
			return 0
		}
	}

	sock := getenv(socketEnvVar)
	if sock == "" {
		var err error
		sock, err = defaultSocketPath(getenv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "osx-bridge-api: %v\n", err)
			return 1
		}
	}

	var provider calendarapi.Provider
	if real, err := eventkitprovider.New(); err != nil {
		// A live TCC denial or an unsupported platform both land here.
		// Logged, not fatal: the daemon still starts and serves the
		// calendar service via unavailableProvider below, which reports
		// every calendar/events call as unavailable/unauthenticated until
		// access is granted and the daemon is restarted (`launchctl
		// kickstart`, AC #4) — a caller gets a well-formed wire error
		// instead of the daemon refusing to even come up.
		fmt.Fprintf(os.Stderr, "osx-bridge-api: calendar provider unavailable: %v\n", err)
		provider = unavailableProvider{err: err}
	} else {
		provider = real
	}

	srv := socketserver.New()
	srv.Register(calendarapi.ServiceName, calendarapi.New(provider))

	ln, err := listen(sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "osx-bridge-api: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("osx-bridge-api %s: listening on %s\n", Version, sock)
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintf(os.Stderr, "osx-bridge-api: serve: %v\n", err)
		return 1
	}
	fmt.Println("osx-bridge-api: shutdown complete")
	return 0
}

// defaultSocketPath resolves the daemon's socket path when the LaunchAgent
// script did not export one: ${XDG_STATE_HOME:-$HOME/.local/state}/osx-bridge-api/osx-bridge-api.sock,
// matching pa-monitor's/pg-router's own darwin-module XDG_STATE_HOME
// convention (see darwin/modules/pa-monitor/default.nix's stateHome).
func defaultSocketPath(getenv func(string) string) (string, error) {
	stateHome := getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("neither " + socketEnvVar + ", XDG_STATE_HOME, nor HOME is set")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "osx-bridge-api", "osx-bridge-api.sock"), nil
}

// listen creates the socket's parent directory, removes a stale socket
// file left behind by a prior (crashed or bootout'd) instance, and starts
// listening. A stale socket is safe to remove outright: nothing else on
// this machine is meant to hold this path, and a launchd KeepAlive
// LaunchAgent (this daemon's only deployment) never has two instances
// racing for the same socket at once.
func listen(sock string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %s: %w", sock, err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", sock, err)
	}
	return ln, nil
}
