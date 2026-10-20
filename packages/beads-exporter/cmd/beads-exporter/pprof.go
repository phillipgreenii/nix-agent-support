package main

import (
	"io"

	"github.com/phillipgreenii/x/pprofserver"
)

// pprofEnv names the environment variable that opts the exporter's serve path
// into the opt-in pprof endpoint. Unset (the default) opens no listener; the
// darwin launchd registration sets it to a 0600 unix socket under the
// exporter's state dir (darwin/modules/beads-exporter/default.nix, whose
// launchd script creates that directory before starting the exporter). The
// value rules (absolute socket path or loopback host:port, otherwise refused
// with one line on stderr) are pprofserver's, not re-implemented here.
const pprofEnv = "BEADS_EXPORTER_PPROF_ADDR"

// startPprofFromEnv starts the profiling endpoint when pprofEnv is set and
// returns the stop function to defer. w receives the one-line "listening" or
// "ignored" report, so callers pass stderr (stdout is the JSON log stream): a
// rejected value never stops the daemon.
func startPprofFromEnv(getenv func(string) string, w io.Writer) (stop func()) {
	return pprofserver.StartFromEnv(pprofEnv, getenv, w)
}
