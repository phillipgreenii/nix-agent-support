package runner

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// procSpec describes one child process: the command, a handler or a verify.
type procSpec struct {
	path string   // resolved executable
	argv []string // argv[0] included
	dir  string
	env  []string
	// stdin is the child's stdin; nil means /dev/null.
	stdin io.Reader
	// stdout and stderr receive the child's streams; they must never
	// return an error, or the copier would stop and the child could block on a
	// full pipe.
	stdout, stderr io.Writer
	// ownGroup puts the child in its own process group and, after it exits,
	// kills whatever is left of that group.
	ownGroup bool
	// timeout stops the child (SIGTERM, then SIGKILL after the kill grace)
	// when it elapses; 0 means no limit.
	timeout time.Duration
	// forward says a wrapper signal is passed on to the child, which is then
	// waited for (the command). Otherwise a wrapper signal terminates the
	// child (a handler or a verify).
	forward bool
	// merge sends the child's stderr down the same pipe as its stdout (to the
	// stdout sink), so the two streams keep their order.
	merge bool
}

// procResult is how a child ended.
type procResult struct {
	startErr error
	// exit is the exit code; -1 when a signal killed the child.
	exit int
	// signal is the signal that killed the child, or 0.
	signal   syscall.Signal
	timedOut bool
	// interrupted says a wrapper signal stopped the child (a handler or a
	// verify; the command is forwarded the signal and waited for instead).
	interrupted bool
}

// runProc starts the child and waits for it, honoring the timeout and the
// wrapper's own signals. Streams are read through pipes the wrapper owns, so
// a grandchild that outlives the child can never wedge the wrapper: after a
// group-owning child exits the whole group is killed and the pipes are given a
// bounded time to drain.
func (r *run) runProc(s procSpec) procResult {
	cmd := &exec.Cmd{Path: s.path, Args: s.argv, Dir: s.dir, Env: s.env}
	if s.stdin != nil {
		cmd.Stdin = s.stdin
	}
	if s.ownGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	type reader struct {
		from *os.File
		to   io.Writer
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return procResult{startErr: err}
	}
	readers := []reader{{outR, s.stdout}}
	writers := []*os.File{outW}
	cmd.Stdout, cmd.Stderr = outW, outW
	if !s.merge {
		errR, errW, err := os.Pipe()
		if err != nil {
			_ = outR.Close()
			_ = outW.Close()
			return procResult{startErr: err}
		}
		readers = append(readers, reader{errR, s.stderr})
		writers = append(writers, errW)
		cmd.Stderr = errW
	}
	err = cmd.Start()
	for _, w := range writers {
		_ = w.Close()
	}
	if err != nil {
		for _, rd := range readers {
			_ = rd.from.Close()
		}
		return procResult{startErr: err}
	}
	defer func() {
		for _, rd := range readers {
			_ = rd.from.Close()
		}
	}()

	var wg sync.WaitGroup
	for _, c := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(c.to, c.from)
		}()
	}
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var res procResult
	var timer <-chan time.Time
	if s.timeout > 0 {
		t := time.NewTimer(s.timeout)
		defer t.Stop()
		timer = t.C
	}
	exited := false
	for !exited {
		select {
		case <-waitCh:
			exited = true
		case <-timer:
			res.timedOut = true
			r.terminate(cmd, s.ownGroup, waitCh)
			exited = true
		case sig := <-r.sigs:
			r.noteSignal(sig)
			if s.forward {
				r.forward(cmd, sig)
			} else {
				res.interrupted = true
				r.terminate(cmd, s.ownGroup, waitCh)
				exited = true
			}
		}
	}

	res.exit = cmd.ProcessState.ExitCode()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.signal = ws.Signal()
		res.exit = -1
	}
	if s.ownGroup {
		r.sweepGroup(cmd.Process.Pid)
	}

	// Give the readers a chance to finish. A group-owning child's pipes close
	// once its group is dead; a grandchild that escaped the group (setsid)
	// must not hold the wrapper hostage, so that wait is bounded. For the
	// command and verify (which stay in the wrapper's group, like a shell
	// pipeline) the wait is as long as the pipes stay open, but a wrapper
	// signal ends it.
	var bound <-chan time.Time
	if s.ownGroup {
		t := time.NewTimer(2*r.p.KillGrace + 250*time.Millisecond)
		defer t.Stop()
		bound = t.C
	}
	select {
	case <-drained:
	case <-bound:
	case sig := <-r.sigs:
		r.noteSignal(sig)
	}
	// Whatever is still open is abandoned, and the copiers are joined, so
	// nothing writes into the caller's sinks after runProc returns.
	for _, rd := range readers {
		_ = rd.from.Close()
	}
	<-drained
	return res
}

// terminate sends SIGTERM (to the child's process group when it owns one,
// otherwise to the child) and, if the child is still there after the kill
// grace, SIGKILL. It returns once the child has been reaped.
func (r *run) terminate(cmd *exec.Cmd, group bool, waitCh <-chan error) {
	r.signalChild(cmd, group, syscall.SIGTERM)
	grace := time.NewTimer(r.p.KillGrace)
	defer grace.Stop()
	select {
	case <-waitCh:
		return
	case <-grace.C:
	}
	r.signalChild(cmd, group, syscall.SIGKILL)
	<-waitCh
}

func (r *run) signalChild(cmd *exec.Cmd, group bool, sig syscall.Signal) {
	if group {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
		return
	}
	_ = cmd.Process.Signal(sig)
}

// forward passes a wrapper signal on to the command. A SIGINT typed at the
// terminal already reached the command, because both are in the terminal's
// foreground process group, so it is not sent twice.
func (r *run) forward(cmd *exec.Cmd, sig os.Signal) {
	if sig == syscall.SIGINT && r.p.Foreground() {
		return
	}
	_ = cmd.Process.Signal(sig)
}

// sweepGroup kills what is left of a process group whose leader has exited:
// SIGTERM, then SIGKILL after the kill grace. A group with no members is left
// alone.
func (r *run) sweepGroup(pgid int) {
	if !groupAlive(pgid) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if !waitGroupGone(pgid, r.p.KillGrace) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		waitGroupGone(pgid, time.Second)
	}
}

func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}

func waitGroupGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !groupAlive(pgid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// signalName names a signal the way a shell user would.
func signalName(s syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
		syscall.SIGILL: "SIGILL", syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT",
		syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL",
		syscall.SIGSEGV: "SIGSEGV", syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM",
		syscall.SIGTERM: "SIGTERM",
	}
	if n, ok := names[s]; ok {
		return n
	}
	return "signal " + strconv.Itoa(int(s))
}
