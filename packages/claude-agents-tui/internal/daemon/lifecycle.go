package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/gofrs/flock"

	"github.com/phillipgreenii/claude-agents-tui/internal/otel"
)

// PIDLock holds the pidfile flock for the lifetime of the daemon process.
// Release MUST be called to remove the file and free the lock. Safe to
// call multiple times.
type PIDLock struct {
	file     *flock.Flock
	path     string
	released bool
}

// AcquirePIDFile creates Paths.Dir if missing, opens the pidfile, takes
// a non-blocking exclusive flock, and writes the current pid into the
// file.
//
// If a previous daemon died without releasing the lock, the kernel has
// already freed it — TryLock will succeed and we overwrite the stale
// pid content. No explicit stale-detection is needed for that case.
//
// Returns an error if the lock is held by a LIVE process.
func AcquirePIDFile(p Paths) (*PIDLock, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}

	fl := flock.New(p.PIDFile)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("flock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("pidfile %s is locked by another process", p.PIDFile)
	}

	pid := []byte(strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(p.PIDFile, pid, 0o600); err != nil {
		_ = fl.Unlock()
		return nil, fmt.Errorf("write pid: %w", err)
	}

	return &PIDLock{file: fl, path: p.PIDFile}, nil
}

// Release frees the lock and removes the pidfile. Safe to call multiple
// times; subsequent calls are no-ops.
func (l *PIDLock) Release() {
	if l == nil || l.released {
		return
	}
	l.released = true
	_ = l.file.Unlock()
	_ = os.Remove(l.path)
}

// BindSocket removes any pre-existing socket file at p.Socket, binds a
// fresh Unix listener, and chmods it 0600. The returned listener removes
// the socket file on Close.
func BindSocket(p Paths) (net.Listener, error) {
	if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	l, err := net.Listen("unix", p.Socket)
	if err != nil {
		return nil, fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return &socketListener{Listener: l, path: p.Socket}, nil
}

// socketListener wraps net.Listener so that Close unlinks the socket
// file in addition to closing the underlying fd.
type socketListener struct {
	net.Listener
	path string
}

func (s *socketListener) Close() error {
	err := s.Listener.Close()
	_ = os.Remove(s.path)
	return err
}

// RunOptions configures a daemon run. Paths is required; everything else
// is optional. Emitter, when non-nil, is shut down on Run return so any
// batched metrics/logs flush before the process exits.
//
// Tick controls a placeholder poll cadence; the real poller + tracker
// integration lands in Plan 3 alongside the client refactor.
type RunOptions struct {
	Paths   Paths
	Emitter *otel.Emitter
	Tick    time.Duration
}

// RunWith is the daemon's main loop. It acquires the pidfile, binds the
// socket, starts the gRPC server, and blocks until ctx is done.
func RunWith(ctx context.Context, opts RunOptions) error {
	lock, err := AcquirePIDFile(opts.Paths)
	if err != nil {
		return err
	}
	defer lock.Release()

	lis, err := BindSocket(opts.Paths)
	if err != nil {
		return err
	}
	defer lis.Close()

	_, stop := serve(lis)
	defer stop()

	defer opts.Emitter.Shutdown(context.Background())

	tick := opts.Tick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			// Plan 3 plumbs the poller + trackers + label cache through
			// here. For Plan 2 the tick just keeps the loop alive so
			// emitter callbacks (when wired in Plan 3) have something
			// to observe.
		}
	}
}

// Run is a thin compat wrapper preserving the original signature used by
// lifecycle_test.go and any caller that doesn't need RunOptions yet.
func Run(ctx context.Context, p Paths) error {
	return RunWith(ctx, RunOptions{Paths: p})
}
