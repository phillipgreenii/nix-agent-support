package originguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ChildSpec describes the wrapped command. stdin and stdout are always
// inherited from the wrapper (it relays nothing). Stderr nil means inherited.
type ChildSpec struct {
	Argv []string
	// OwnGroup puts the child in its own process group.
	OwnGroup bool
	Stderr   io.Writer
}

// Child is a started wrapped command.
type Child interface {
	// Pid is the child's pid; with OwnGroup it is also the group id.
	Pid() int
	// Done is closed when the child has ended and its output is drained.
	Done() <-chan struct{}
	// ExitCode is valid after Done: the exit status, or 128+signal.
	ExitCode() int
	// Signal sends sig to the child's group when it has one, else the child.
	Signal(sig syscall.Signal) error
}

// Spawner starts the wrapped command. The wrapper supervises a streaming
// process (inherited stdio, signals, process groups), so it cannot use the
// buffered cmdrun.Runner; this is its own seam. The real implementation is
// ExecSpawner.
type Spawner interface {
	Start(ChildSpec) (Child, error)
}

// ExecSpawner is the real Spawner.
type ExecSpawner struct{}

type execChild struct {
	cmd      *exec.Cmd
	ownGroup bool
	done     chan struct{}
	code     int
}

// Start runs spec.Argv with inherited stdin and stdout.
func (ExecSpawner) Start(spec ChildSpec) (Child, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("no command")
	}
	cmd := exec.CommandContext(context.Background(), spec.Argv[0], spec.Argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if spec.Stderr != nil {
		cmd.Stderr = spec.Stderr
		cmd.WaitDelay = 2 * time.Second
	}
	if spec.OwnGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &execChild{cmd: cmd, ownGroup: spec.OwnGroup, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		c.code = exitCodeOf(cmd.ProcessState)
		close(c.done)
	}()
	return c, nil
}

func exitCodeOf(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func (c *execChild) Pid() int              { return c.cmd.Process.Pid }
func (c *execChild) Done() <-chan struct{} { return c.done }
func (c *execChild) ExitCode() int         { return c.code }

func (c *execChild) Signal(sig syscall.Signal) error {
	if c.ownGroup {
		return syscall.Kill(-c.cmd.Process.Pid, sig)
	}
	return syscall.Kill(c.cmd.Process.Pid, sig)
}

// Procs identifies and signals another process, for verified preemption.
type Procs interface {
	// Token is a start-time token of pid, or "" when pid is gone. A reused
	// pid has a different token.
	Token(pid int) string
	// KillGroup signals the process group pid.
	KillGroup(pid int, sig syscall.Signal) error
}

// OSProcs is the real Procs. It reads the start time with ps.
type OSProcs struct{}

// Token returns `ps -o lstart=` of pid.
func (OSProcs) Token(pid int) string {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// KillGroup sends sig to the group.
func (OSProcs) KillGroup(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }

// Timers is the timer seam; tests drive it by hand.
type Timers interface {
	After(d time.Duration) <-chan time.Time
}

// RealTimers uses time.After.
type RealTimers struct{}

// After returns time.After(d).
func (RealTimers) After(d time.Duration) <-chan time.Time { return time.After(d) }

// tailBuf keeps the last N bytes written to it.
type tailBuf struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func refusalLine(r Reason) string { return fmt.Sprintf("pg-origin-guard: refused (%s)\n", r) }
