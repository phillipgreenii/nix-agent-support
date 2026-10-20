package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/x/pprofserver"
)

// pprofEnv names the environment variable that opts the serve mode into the
// opt-in pprof endpoint. It is an operator diagnostic, not a setting: unset
// (the default) opens no listener, the config file never carries it, and the pr,
// ci and status modes never read it. The Home-Manager module's launchd
// registration sets it to a 0600 unix socket under the daemon's state
// directory (home/programs/pg-connector/default.nix, github.pprof.enable). The
// value rules (absolute socket path or loopback host:port, otherwise refused
// with one line on stderr) are pprofserver's, not re-implemented here.
const pprofEnv = "PG_CONNECTOR_GITHUB_PPROF_ADDR"

// startPprofFromEnv starts the profiling endpoint when pprofEnv is set and
// returns the stop function to defer. w receives the one-line "listening" or
// "ignored" report, so callers pass stderr: a rejected value never stops the
// daemon. pprofserver does not create the socket's parent directory, so for an
// absolute path it is created here (mode 0700, as the daemon's other state
// directories); a failure to create it is left for pprofserver to report as the
// listen error.
func startPprofFromEnv(getenv func(string) string, w io.Writer) (stop func()) {
	if addr := getenv(pprofEnv); filepath.IsAbs(addr) {
		_ = os.MkdirAll(filepath.Dir(addr), 0o700)
	}
	return pprofserver.StartFromEnv(pprofEnv, getenv, w)
}
