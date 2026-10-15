package keeper

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// ProcControl is the Reconciler's seam over processes: start, signal, wait,
// enumerate. Production is OSProcs; tests substitute a fake that records the
// call sequence, which is how the make-before-break ordering and the "never
// touches a different state_dir" rule are asserted.
type ProcControl interface {
	// Start launches argv (argv[0] is the executable's absolute path) detached
	// in its own session and process group, and returns its pid. The
	// implementation MUST Wait() on the child it started.
	Start(argv []string) (pid int, err error)
	// Signal sends sig to pid.
	Signal(pid int, sig syscall.Signal) error
	// WaitExit waits up to timeout for pid to exit and reports whether it did.
	WaitExit(pid int, timeout time.Duration) bool
	// Probe reports pid's existence, start time and zombie state.
	Probe(pid int) lease.ProcState
	// Info reports pid's executable name and argv.
	Info(pid int) (lease.ProcInfo, error)
	// PIDs enumerates every pid in the process table.
	PIDs() ([]int, error)
}

// OSProcs is the production ProcControl. Every child it starts is waited on by
// a goroutine, so an exited child never lingers as a zombie; a pid adopted
// from an earlier keeper is not a child and is checked by state instead.
type OSProcs struct {
	mu       sync.Mutex
	children map[int]chan struct{}
}

// NewOSProcs returns the production ProcControl.
func NewOSProcs() *OSProcs { return &OSProcs{children: map[int]chan struct{}{}} }

// Start implements ProcControl. The child gets its own session (Setsid, so
// also its own process group), no stdin, stdout or stderr, and the keeper's
// environment (which is how the hidden battery-source variable reaches the
// anchor).
func (o *OSProcs) Start(argv []string) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, errors.New("keeper: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is built by the keeper, never from a lease file
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("keeper: start %s: %w", argv[0], err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	o.mu.Lock()
	o.children[pid] = done
	o.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(done)
		o.mu.Lock()
		delete(o.children, pid)
		o.mu.Unlock()
	}()
	return pid, nil
}

// Signal implements ProcControl.
func (o *OSProcs) Signal(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return fmt.Errorf("keeper: refusing to signal pid %d", pid)
	}
	return syscall.Kill(pid, sig)
}

// WaitExit implements ProcControl.
func (o *OSProcs) WaitExit(pid int, timeout time.Duration) bool {
	o.mu.Lock()
	done, child := o.children[pid]
	o.mu.Unlock()
	if child {
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			return false
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		if st := (lease.SysProber{}).Probe(pid); !st.Exists || st.Zombie {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Probe implements ProcControl.
func (o *OSProcs) Probe(pid int) lease.ProcState { return lease.SysProber{}.Probe(pid) }

// Info implements ProcControl.
func (o *OSProcs) Info(pid int) (lease.ProcInfo, error) { return lease.ProcessInfo(pid) }

// PIDs implements ProcControl.
func (o *OSProcs) PIDs() ([]int, error) { return lease.ListPIDs() }
