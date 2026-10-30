package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/x/pprofserver"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
)

// pprofSocket is where the opt-in profiler listens: $XDG_STATE_HOME/<instance>/
// pprof.sock, following pg-connector's option and not stateDir, so the profiler
// socket never shares a directory with the data store. (The design also says
// elsewhere that pprof.sock sits in the state directory; the more specific
// packaging rule wins and the mismatch is recorded in the package README.)
func pprofSocket(getenv func(string) string) string {
	return filepath.Join(config.StateHome(getenv), "pprof.sock")
}

// startPprof starts the opt-in profiling endpoint and returns its stop
// function. It is off unless --pprof is given or PG_CLAUDE_CODE_RECORDER_PPROF_ADDR
// is set (the module's launchd registration sets it, docs/go-daemons.md in
// phillipg-nix-repo-base); the variable wins and carries a socket path or
// loopback host:port. A rejected value never stops the daemon: pprofserver
// reports one line on w.
func startPprof(flagOn bool, getenv func(string) string, w io.Writer) (stop func()) {
	addr := getenv(config.EnvPprofAddr)
	if addr == "" && !flagOn {
		return func() {}
	}
	if addr == "" {
		addr = pprofSocket(getenv)
	}
	if filepath.IsAbs(addr) {
		// pprofserver does not create the socket's parent directory.
		_ = os.MkdirAll(filepath.Dir(addr), 0o700)
	}
	env := func(string) string { return addr }
	return pprofserver.StartFromEnv(config.EnvPprofAddr, env, w)
}
