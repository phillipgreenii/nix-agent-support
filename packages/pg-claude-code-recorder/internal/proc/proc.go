// Package proc is the recorder's view of operating-system processes: a process's
// start time, an exit watch (kqueue NOTE_EXIT on darwin, a one-second liveness
// poll that checks the start time elsewhere) and its environment (the
// KERN_PROCARGS2 sysctl on darwin, /proc elsewhere). The System interface is the
// ProcessTable seam: it is the one legitimate fake in the recorder's tests.
package proc

import (
	"errors"
	"strings"
	"time"
)

// ErrGone means the process does not exist (or no longer has the start time the
// caller expected): registering a watch on an already exited pid fails with it
// (ESRCH), and that failure is the recorded fact.
var ErrGone = errors.New("process is gone")

// System is the process table.
type System interface {
	// StartTime returns the start time of the live process pid, or ErrGone.
	StartTime(pid int) (time.Time, error)
	// Watch calls onExit once, with the time the exit was observed, when pid
	// exits, and returns a stop function. A pid that has already exited fails
	// with ErrGone.
	Watch(pid int, onExit func(at time.Time)) (stop func(), err error)
	// Environ returns the environment of the live process pid.
	Environ(pid int) (map[string]string, error)
}

// StartLayout is the layout of the registry's procStart field and of
// `ps -o lstart=` in UTC ("Wed Oct 14 07:51:50 2026").
const StartLayout = "Mon Jan _2 15:04:05 2006"

// StartTolerance absorbs a second-boundary rounding difference between the
// registry's procStart and the kernel's start time; a pid-reusing process starts
// far later than this.
const StartTolerance = 2 * time.Second

// ParseStart parses a registry procStart as UTC.
func ParseStart(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(StartLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// FormatStart renders a start time the way the registry writes procStart.
func FormatStart(t time.Time) string { return t.UTC().Format(StartLayout) }

// Matches reports whether the live start time agrees with the registry's
// procStart. An empty or unparseable procStart fails open (true), as older
// registries carry none.
func Matches(procStart string, live time.Time) bool {
	want, ok := ParseStart(procStart)
	if !ok {
		return true
	}
	d := live.Sub(want)
	if d < 0 {
		d = -d
	}
	return d <= StartTolerance
}

// Alive reports whether pid is a live process that started when procStart says
// (a reused pid is dead).
func Alive(sys System, pid int, procStart string) bool {
	t, err := sys.StartTime(pid)
	if err != nil {
		return false
	}
	return Matches(procStart, t)
}

// Host is the binding of a Process to where it takes input, read once from its
// environment at its start.
type Host struct {
	// Kind is cmux, tmux or other.
	Kind          string `json:"kind"`
	CmuxSurface   string `json:"cmux_surface_id,omitempty"`
	CmuxWorkspace string `json:"cmux_workspace_id,omitempty"`
	TmuxSocket    string `json:"tmux_socket,omitempty"`
	TmuxPane      string `json:"tmux_pane,omitempty"`
	TermProgram   string `json:"term_program,omitempty"`
}

// HostOf reads the host binding from an environment: for cmux the surface and
// workspace ids, for tmux the socket path from TMUX plus the pane id (the pane id
// alone is ambiguous across sockets), and otherwise only TERM_PROGRAM.
func HostOf(env map[string]string) Host {
	h := Host{Kind: "other", TermProgram: env["TERM_PROGRAM"]}
	switch {
	case env["CMUX_SURFACE_ID"] != "":
		h.Kind, h.CmuxSurface, h.CmuxWorkspace = "cmux", env["CMUX_SURFACE_ID"], env["CMUX_WORKSPACE_ID"]
	case env["TMUX"] != "":
		sock, _, _ := strings.Cut(env["TMUX"], ",")
		h.Kind, h.TmuxSocket, h.TmuxPane = "tmux", sock, env["TMUX_PANE"]
	}
	return h
}

// parseProcArgs2 extracts the environment from a darwin KERN_PROCARGS2 buffer:
// argc (int32), the executable path, NUL padding, argc argument strings, then
// the environment strings up to the first empty one.
func parseProcArgs2(b []byte) map[string]string {
	env := map[string]string{}
	if len(b) < 4 {
		return env
	}
	argc := int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 // little-endian on every darwin target
	i := 4
	for i < len(b) && b[i] != 0 { // the executable path
		i++
	}
	for i < len(b) && b[i] == 0 { // padding
		i++
	}
	for n := 0; n < argc && i < len(b); n++ { // the arguments
		for i < len(b) && b[i] != 0 {
			i++
		}
		i++
	}
	for i < len(b) {
		j := i
		for j < len(b) && b[j] != 0 {
			j++
		}
		if j == i {
			break
		}
		if k := strings.IndexByte(string(b[i:j]), '='); k > 0 {
			env[string(b[i:i+k])] = string(b[i+k+1 : j])
		}
		i = j + 1
	}
	return env
}
