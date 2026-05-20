package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// PathOverrides allows callers (tests, CLI flags) to override individual
// file paths. Empty fields fall back to the XDG-derived defaults.
type PathOverrides struct {
	Socket  string
	PIDFile string
}

// Paths holds every path the daemon needs on disk.
type Paths struct {
	Dir     string // parent directory; daemon may create
	Socket  string
	PIDFile string
}

// ResolvePaths picks Dir per spec:
//   - Linux: $XDG_RUNTIME_DIR/pa-monitor
//   - macOS: $XDG_STATE_HOME/pa-monitor  (XDG_RUNTIME_DIR is not standard there)
//
// Both Socket and PIDFile live inside Dir. Overrides win unconditionally.
func ResolvePaths(o PathOverrides) (Paths, error) {
	dir, err := defaultDir()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{
		Dir:     dir,
		Socket:  filepath.Join(dir, "daemon.sock"),
		PIDFile: filepath.Join(dir, "daemon.pid"),
	}
	if o.Socket != "" {
		p.Socket = o.Socket
	}
	if o.PIDFile != "" {
		p.PIDFile = o.PIDFile
	}
	return p, nil
}

func defaultDir() (string, error) {
	var base string
	if runtime.GOOS == "darwin" {
		base = os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home := os.Getenv("HOME")
			if home == "" {
				return "", fmt.Errorf("HOME and XDG_STATE_HOME both unset")
			}
			base = filepath.Join(home, ".local", "state")
		}
	} else {
		base = os.Getenv("XDG_RUNTIME_DIR")
		if base == "" {
			base = os.Getenv("XDG_STATE_HOME")
		}
		if base == "" {
			home := os.Getenv("HOME")
			if home == "" {
				return "", fmt.Errorf("HOME, XDG_RUNTIME_DIR, and XDG_STATE_HOME all unset")
			}
			base = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(base, "pa-monitor"), nil
}
