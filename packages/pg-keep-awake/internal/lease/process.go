package lease

import "errors"

// ErrNoProcess means the pid does not exist.
var ErrNoProcess = errors.New("no such process")

// ProcState is what a Prober learned about one pid.
type ProcState struct {
	// Exists is false when the pid is not in the process table.
	Exists bool
	// StartUS is p_starttime in microseconds since the epoch (0 when !Exists).
	StartUS int64
	// Zombie is true for SZOMB: the process has exited but is not yet reaped,
	// so it MUST NOT count as live.
	Zombie bool
}

// Prober looks a pid up. SysProber is the real one; tests and other packets
// may substitute a fake to exercise the liveness predicate.
type Prober interface {
	Probe(pid int) ProcState
}

// SysProber probes through sysctl KERN_PROC_PID (never by forking ps).
type SysProber struct{}

// Probe implements Prober. A pid of 0 or less does not exist.
func (SysProber) Probe(pid int) ProcState {
	if pid <= 0 {
		return ProcState{}
	}
	st, err := ProcStateOf(pid)
	if err != nil {
		return ProcState{}
	}
	return st
}

// ProcInfo is a process's executable name and argv.
type ProcInfo struct {
	// Name is the basename of the full executable path from KERN_PROCARGS2,
	// never the 16-byte truncated p_comm, so a name over 16 characters
	// (pg-caffeinate-shim) matches.
	Name string
	Argv []string
}

// Ref returns the {pid, start_us} reference for pid.
func Ref(pid int) (ProcRef, error) {
	st, err := ProcStateOf(pid)
	if err != nil {
		return ProcRef{}, err
	}
	return ProcRef{PID: pid, StartUS: st.StartUS}, nil
}
