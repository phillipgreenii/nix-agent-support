package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/shimargs"
)

// Poll intervals. Timers are checked against the wall clock at least this
// often, because Go's monotonic timers do not count time the Mac spends
// asleep, and -t counts wall-clock time.
const (
	maxPoll   = time.Second
	watchPoll = 200 * time.Millisecond
)

// parseArgs runs the caffeinate-compatible parser with the real process table.
func parseArgs(args []string) (shimargs.Request, *shimargs.Outcome) {
	return shimargs.Parse(args, func(pid int) bool { return lease.SysProber{}.Probe(pid).Exists })
}

// holder is a lease this shim wrote and must remove.
type holder struct {
	dir    *lease.Dir
	id     string
	stderr io.Writer
}

// remove deletes the lease; a failure is reported but never changes the exit.
func (h *holder) remove() {
	if h == nil {
		return
	}
	if err := h.dir.RemoveLease(h.id); err != nil {
		fmt.Fprintf(h.stderr, "caffeinate: warning: could not remove lease %s: %v\n", h.id, err)
	}
}

// flagList renders the flag set as lease flags in d, i, m, s order.
func flagList(f shimargs.Flags) []string {
	var out []string
	for _, c := range f.String() {
		out = append(out, string(c))
	}
	return out
}

// newLease builds the shim lease for req, bound to self. The expiry is a plain
// wall-clock time (no monotonic reading), so every later comparison uses the
// wall clock.
func newLease(req shimargs.Request, args []string, self lease.ProcRef, now time.Time) *lease.Lease {
	l := &lease.Lease{
		Owner:     lease.ShimOwner,
		Key:       fmt.Sprintf("%d-%d", self.PID, self.StartUS),
		Kind:      lease.KindShim,
		Client:    &self,
		Flags:     flagList(req.Flags),
		Argv:      append([]string{"caffeinate"}, args...),
		CreatedAt: now,
	}
	if req.Timeout.Kind == shimargs.TimeoutFinite {
		exp := now.Add(req.Timeout.D).Round(0)
		l.ExpiresAt = &exp
	}
	return l
}

// writeLease creates the state directory when needed and writes l.
func writeLease(l *lease.Lease, stderr io.Writer) (*holder, error) {
	path, err := lease.ResolveStateDir()
	if err != nil {
		return nil, err
	}
	d, err := lease.EnsureDir(path)
	if err != nil {
		return nil, err
	}
	if err := d.PutLease(l); err != nil {
		return nil, err
	}
	return &holder{dir: d, id: l.ID, stderr: stderr}, nil
}

// signalSet is the signals the shim reacts to.
var signalSet = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// subscribe asks for the signals the shim handles, skipping any that was
// ignored at start: installing a handler for one would defeat the ignore (a
// non-interactive background job keeps SIGINT ignored, as the real tool does).
func subscribe() (chan os.Signal, func()) {
	ch := make(chan os.Signal, 8)
	var want []os.Signal
	for _, s := range signalSet {
		if !signal.Ignored(s) {
			want = append(want, s)
		}
	}
	if len(want) > 0 {
		signal.Notify(ch, want...)
	}
	return ch, func() { signal.Stop(ch) }
}

// runPure is the form with no utility: hold the lease until -t expires, the -w
// process exits, or a signal arrives.
func runPure(req shimargs.Request, args []string, stderr io.Writer) result {
	self, err := lease.Ref(os.Getpid())
	if err != nil {
		fmt.Fprintf(stderr, "caffeinate: cannot identify this process: %v\n", err)
		return result{code: exitLeaseWrite}
	}
	now := time.Now().UTC()
	l := newLease(req, args, self, now)
	var watch *lease.ProcRef
	if req.Watch.Kind == shimargs.WatchPID {
		ref, err := lease.Ref(req.Watch.PID)
		if err != nil || !watchAlive(ref) {
			return result{} // the watched process is already gone: no lease, exit 0
		}
		watch = &ref
		l.Watch = watch
	}

	// Subscribe before the lease exists, so a signal arriving in between is
	// held for the loop instead of killing the shim with the file in place.
	sigs, stop := subscribe()
	defer stop()

	h, err := writeLease(l, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "caffeinate: cannot write keep-awake lease: %v\n", flatten(err))
		return result{code: exitLeaseWrite}
	}

	var res result
	select {
	case s := <-sigs:
		res = signalResult(s)
	case <-waitBounds(l.ExpiresAt, watch):
	}
	h.remove()
	return res
}

// waitBounds returns a channel that closes when the expiry passes or the
// watched process exits; with neither it never closes. The check runs on the
// wall clock at the poll intervals above.
func waitBounds(expires *time.Time, watch *lease.ProcRef) <-chan struct{} {
	done := make(chan struct{})
	if expires == nil && watch == nil {
		return done
	}
	go func() {
		defer close(done)
		for {
			wait := maxPoll
			if watch != nil {
				wait = watchPoll
			}
			if expires != nil {
				if rem := time.Until(*expires); rem < wait {
					wait = max(rem, 0)
				}
			}
			time.Sleep(wait)
			if expires != nil && !time.Now().Before(*expires) {
				return
			}
			if watch != nil && !watchAlive(*watch) {
				return
			}
		}
	}()
	return done
}

// watchAlive reports whether the watched process still exists with its start
// time. A zombie has exited, so it counts as gone (the real tool's kqueue
// watch fires on exit, not on reaping).
func watchAlive(w lease.ProcRef) bool {
	st := lease.SysProber{}.Probe(w.PID)
	return st.Exists && st.StartUS == w.StartUS && !st.Zombie
}

func signalResult(s os.Signal) result {
	if sig, ok := s.(syscall.Signal); ok {
		return result{sig: sig}
	}
	return result{code: 1}
}

// flatten keeps an error on one line.
func flatten(err error) string {
	b := []byte(err.Error())
	for i, c := range b {
		if c == '\n' || c == '\r' {
			b[i] = ' '
		}
	}
	return string(b)
}

// runUtility is the utility form: run the command as a child, hold the lease
// for the child's life, forward signals, and exit as the child did. -t and -w
// do not apply (the parser leaves them zero).
func runUtility(req shimargs.Request, args []string, stderr io.Writer) result {
	name := req.Utility[0]
	if _, err := exec.LookPath(name); err != nil {
		return startFailure(name, err, stderr)
	}
	cmd := exec.Command(name, req.Utility[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	sigs, stop := subscribe()
	defer stop()

	var h *holder
	if self, err := lease.Ref(os.Getpid()); err != nil {
		fmt.Fprintf(stderr, "caffeinate: warning: cannot hold a keep-awake lease: %v\n", flatten(err))
	} else if h, err = writeLease(newLease(req, args, self, time.Now().UTC()), stderr); err != nil {
		h = nil
		fmt.Fprintf(stderr, "caffeinate: warning: cannot hold a keep-awake lease: %v\n", flatten(err))
	}

	if err := cmd.Start(); err != nil {
		h.remove()
		return startFailure(name, err, stderr)
	}
	done := make(chan struct{})
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	<-forwarded
	h.remove()

	if waitErr == nil {
		return result{}
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		fmt.Fprintf(stderr, "caffeinate: %s: %v\n", name, waitErr)
		return result{code: 1}
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return result{sig: ws.Signal()}
	}
	return result{code: ee.ExitCode()}
}

// startFailure reports a utility that could not be started, with the shell's
// exit conventions.
func startFailure(name string, err error, stderr io.Writer) result {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(stderr, "%s: No such file or directory\n", name)
		return result{code: exitNotFound}
	case errors.Is(err, fs.ErrPermission):
		fmt.Fprintf(stderr, "%s: Permission denied\n", name)
		return result{code: exitNotRunable}
	}
	fmt.Fprintf(stderr, "%s: %v\n", name, err)
	return result{code: exitNotRunable}
}
