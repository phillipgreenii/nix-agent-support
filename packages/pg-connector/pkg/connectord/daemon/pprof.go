package daemon

import (
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/x/pprofserver"
)

// StartPprof starts the opt-in profiling endpoint when the environment variable
// env is set and returns the stop function to defer. The variable is an
// operator diagnostic, not a setting: unset (the default) opens no listener.
// w receives the one-line "listening" or "ignored" report, so callers pass
// stderr: a rejected value never stops the daemon. The value rules (absolute
// socket path or loopback host:port, otherwise refused with one line) are
// pprofserver's. pprofserver does not create the socket's parent directory, so
// for an absolute path it is created here (mode 0700, as the daemon's other
// state directories); a failure to create it is left for pprofserver to report
// as the listen error.
func StartPprof(env string, getenv func(string) string, w io.Writer) (stop func()) {
	if addr := getenv(env); filepath.IsAbs(addr) {
		_ = os.MkdirAll(filepath.Dir(addr), 0o700)
	}
	return pprofserver.StartFromEnv(env, getenv, w)
}
