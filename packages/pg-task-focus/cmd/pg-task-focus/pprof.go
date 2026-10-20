package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/x/pprofserver"
)

// pprofEnv names the environment variable that opts `pg-task-focus serve` into the
// opt-in pprof endpoint. Unset (the default) opens no listener; the darwin
// launchd registration sets it to a 0600 unix socket under the daemon's state
// dir (home/programs/pg-task-focus/default.nix). The value rules (absolute
// socket path or loopback host:port, otherwise refused with one line on
// stderr) are pprofserver's, not re-implemented here.
const pprofEnv = "PG_TASK_FOCUS_PPROF_ADDR"

// startPprofFromEnv starts the profiling endpoint when pprofEnv is set and
// returns the stop function to defer. w receives the one-line "listening" or
// "ignored" report, so callers pass stderr: a rejected value never stops the
// daemon.
//
// pprofserver does not create the socket's parent directory, and serve does
// not create the state directory itself (launchd creates the log files'
// directory only when it opens them), so for an absolute path the helper
// creates the parent first; a failure here surfaces as pprofserver's own
// "ignored" line when Start cannot bind.
func startPprofFromEnv(getenv func(string) string, w io.Writer) (stop func()) {
	if addr := getenv(pprofEnv); filepath.IsAbs(addr) {
		_ = os.MkdirAll(filepath.Dir(addr), 0o755)
	}
	return pprofserver.StartFromEnv(pprofEnv, getenv, w)
}
