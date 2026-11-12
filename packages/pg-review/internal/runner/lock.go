package runner

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProcInspector reads a process's start time. The reaper compares it with the
// start time stored on the run row, so a recycled pid is not mistaken for the
// original process.
type ProcInspector interface {
	// StartTime returns the process start time, or ok=false when no process
	// with that pid exists.
	StartTime(pid int) (start time.Time, ok bool)
}

// PSInspector reads the start time with `ps -o lstart= -p <pid>` (second
// resolution, local time).
type PSInspector struct{}

// StartTime implements ProcInspector.
func (PSInspector) StartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	s := strings.Join(strings.Fields(string(out)), " ")
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// alive reports whether the run's recorded process is still the same
// process: it exists and, when a start time was stored, started then.
func alive(p ProcInspector, pid int, stored time.Time) bool {
	if pid <= 0 {
		return false
	}
	got, ok := p.StartTime(pid)
	if !ok {
		return false
	}
	if stored.IsZero() {
		return true
	}
	return got.Equal(stored)
}
