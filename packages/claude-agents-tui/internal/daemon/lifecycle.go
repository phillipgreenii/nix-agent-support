package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/gofrs/flock"
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

// Run is the daemon's main loop. It acquires the pidfile, binds the
// socket, and blocks until ctx is done. On return — for any reason —
// both the pidfile and socket file are removed.
//
// In this foundation milestone Run has no gRPC server attached yet.
// Task 24 adds the server skeleton; this function gains a call to start
// it then.
func Run(ctx context.Context, p Paths) error {
	lock, err := AcquirePIDFile(p)
	if err != nil {
		return err
	}
	defer lock.Release()

	lis, err := BindSocket(p)
	if err != nil {
		return err
	}
	defer lis.Close()

	<-ctx.Done()
	return nil
}
