//go:build unix

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
)

// Defaults of the process-control seams.
const (
	DefaultGrace     = 5 * time.Second
	DefaultWaitDelay = 2 * time.Second
)

// baseEnvNames are the caller environment variables every generator receives
// (when set in the caller's environment).
var baseEnvNames = []string{"PATH", "HOME", "USER", "LANG", "TMPDIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}

// Environ builds the allowlisted environment of one invocation: the base
// names, the generator's own env names (an unset name is omitted, not set
// empty), WORK_REPORT_CONTRACT=1 and WORK_REPORT_MODE=mode. lookup reads the
// caller's environment (os.LookupEnv when nil). config validate uses the same
// function, so validation sees what a real run sees.
func Environ(g config.Generator, mode string, lookup func(string) (string, bool)) []string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	seen := map[string]bool{"WORK_REPORT_CONTRACT": true, "WORK_REPORT_MODE": true}
	var env []string
	for _, names := range [][]string{baseEnvNames, g.Env} {
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			if v, ok := lookup(n); ok {
				env = append(env, n+"="+v)
			}
		}
	}
	return append(env, "WORK_REPORT_CONTRACT=1", "WORK_REPORT_MODE="+mode)
}

// ExecSpec is one process execution.
type ExecSpec struct {
	// Argv is run directly, never through a shell. A bare Argv[0] is resolved
	// on PATH.
	Argv []string
	// Dir is the working directory.
	Dir string
	// Env is the complete environment (see Environ).
	Env []string
	// Stdin is written to the process; a write error on a process that exits
	// 0 without reading it is not a failure.
	Stdin []byte
	// Timeout covers the whole run including writing Stdin; zero means
	// DefaultGeneratorTimeout.
	Timeout time.Duration
}

// ExecResult is what an execution produced.
type ExecResult struct {
	// Stdout is at most StdoutCap bytes; StdoutCapped is true when more was
	// written (the process is then terminated).
	Stdout       []byte
	StdoutCapped bool
	// Stderr is the retained stderr: the first and last StderrKeep bytes with
	// a [truncated N bytes] marker between them.
	Stderr string
	// ExitCode is nil when the process was killed by a signal or never ran.
	ExitCode *int
	// Signal names the terminating signal ("" when none).
	Signal string
	// TimedOut and Cancelled report why the run was terminated.
	TimedOut, Cancelled bool
	// NotFound is true when the executable does not exist.
	NotFound bool
	// StartErr is a failure to start the process (including NotFound).
	StartErr error
	// Duration is the wall time of the execution.
	Duration time.Duration
}

// Exec runs spec: an argv vector with the given environment in a new process
// group, bounded by the timeout and ctx. On expiry or cancellation the group
// receives SIGTERM, then SIGKILL after the grace period; the final Wait is
// bounded by WaitDelay so a grandchild holding a pipe open cannot hang the
// run; and on every exit the remaining members of the group are killed.
func (r *Runner) Exec(ctx context.Context, spec ExecSpec) ExecResult {
	var res ExecResult
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		res.StartErr = errors.New("empty command")
		res.NotFound = true
		return res
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = config.DefaultGeneratorTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var overflowed sync.Once
	stdout := &capWriter{max: StdoutCap, onOverflow: func() { overflowed.Do(cancel) }}
	stderr := &tailWriter{keep: StderrKeep}

	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = bytes.NewReader(spec.Stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = r.waitDelay()

	begin := time.Now()
	if err := cmd.Start(); err != nil {
		res.StartErr = err
		res.NotFound = errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		res.Duration = time.Since(begin)
		return res
	}
	pid := cmd.Process.Pid

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-runCtx.Done():
		killGroup(pid, syscall.SIGTERM)
		timer := time.NewTimer(r.grace())
		select {
		case waitErr = <-done:
			timer.Stop()
		case <-timer.C:
			killGroup(pid, syscall.SIGKILL)
			waitErr = <-done
		}
		switch {
		case stdout.overflowed():
			// reported through StdoutCapped
		case ctx.Err() != nil:
			res.Cancelled = true
		default:
			res.TimedOut = true
		}
	}
	// Whatever way the leader ended, nothing of its group may outlive it
	// before the caller hashes files.
	killGroup(pid, syscall.SIGKILL)
	res.Duration = time.Since(begin)

	if cmd.ProcessState != nil {
		if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			switch {
			case st.Signaled():
				res.Signal = signalName(st.Signal())
			case st.Exited():
				code := st.ExitStatus()
				res.ExitCode = &code
			}
		}
	}
	// waitErr (an exit error, or exec.ErrWaitDelay when a grandchild held a
	// pipe open) is reported through ExitCode and Signal.
	_ = waitErr
	res.Stdout = stdout.bytes()
	res.StdoutCapped = stdout.overflowed()
	res.Stderr = stderr.String()
	return res
}

func killGroup(pid int, sig syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	}
	return fmt.Sprintf("signal %d", int(s))
}

// capWriter keeps at most max bytes; one byte more is an overflow, after which
// further bytes are discarded.
type capWriter struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int
	over       bool
	onOverflow func()
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.over {
		w.mu.Unlock()
		return len(p), nil
	}
	room := w.max - w.buf.Len()
	if len(p) <= room {
		w.buf.Write(p)
		w.mu.Unlock()
		return len(p), nil
	}
	w.buf.Write(p[:room])
	w.over = true
	w.mu.Unlock()
	if w.onOverflow != nil {
		w.onOverflow()
	}
	return len(p), nil
}

func (w *capWriter) overflowed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.over
}

func (w *capWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// tailWriter drains everything written to it but retains only the first and
// last keep bytes.
type tailWriter struct {
	mu    sync.Mutex
	keep  int
	head  []byte
	tail  []byte
	total int64
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.total += int64(n)
	if room := w.keep - len(w.head); room > 0 {
		take := min(room, len(p))
		w.head = append(w.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		w.tail = append(w.tail, p...)
		if len(w.tail) > 2*w.keep {
			w.tail = append([]byte(nil), w.tail[len(w.tail)-w.keep:]...)
		}
	}
	return n, nil
}

// String returns the retained text: everything when it fits in two keeps,
// otherwise the head, a "[truncated N bytes]" marker line and the tail, each
// cut on a UTF-8 boundary.
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.total <= int64(2*w.keep) {
		return string(w.head) + string(w.tail)
	}
	head := w.head
	tail := w.tail
	if len(tail) > w.keep {
		tail = tail[len(tail)-w.keep:]
	}
	// Cut the head back to the last complete rune.
	for i := len(head) - 1; i >= 0 && i >= len(head)-utf8.UTFMax; i-- {
		if utf8.RuneStart(head[i]) {
			if !utf8.FullRune(head[i:]) {
				head = head[:i]
			}
			break
		}
	}
	// Advance the tail to the first rune start.
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	dropped := w.total - int64(len(head)) - int64(len(tail))
	var b strings.Builder
	b.Write(head)
	fmt.Fprintf(&b, "\n[truncated %d bytes]\n", dropped)
	b.Write(tail)
	return b.String()
}

var _ io.Writer = (*capWriter)(nil)
