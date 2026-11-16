package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrBusy reports a lock or hold that another holder keeps.
var ErrBusy = errors.New("busy: held by another process")

const retryEvery = 10 * time.Millisecond

// flockFile opens (creating) path and takes an exclusive flock, retrying until
// wait has passed (wait <= 0 tries once) or ctx ends. The returned func
// releases the lock and closes the file; the OS also releases it if the holder
// dies.
func flockFile(ctx context.Context, path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
		if !time.Now().Add(retryEvery).Before(deadline) {
			_ = f.Close()
			return nil, ErrBusy
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(retryEvery):
		}
	}
}

// Lock takes the repo lock (Dir/lock) with bounded retry and reads the state.
// It returns ErrBusy after wait.
func (s Store) Lock(ctx context.Context, wait time.Duration) (*Locked, error) {
	unlock, err := flockFile(ctx, filepath.Join(s.Dir, "lock"), wait)
	if err != nil {
		return nil, err
	}
	st, err := s.ReadUnlocked()
	if err != nil {
		unlock()
		return nil, err
	}
	return &Locked{s: s, st: st, unl: unlock}, nil
}

func (s Store) opPath(id string) string { return filepath.Join(s.Dir, "ops", id+".lock") }

// HoldOp takes the exclusive hold of operation id (Dir/ops/<id>.lock) and
// fails at once with ErrBusy when another holder has it. The hold dies with
// its process.
func (s Store) HoldOp(id string) (release func(), err error) {
	return flockFile(context.Background(), s.opPath(id), 0)
}

// OpHeld reports whether some holder keeps operation id, by a try-lock probe.
func (s Store) OpHeld(id string) (bool, error) {
	rel, err := flockFile(context.Background(), s.opPath(id), 0)
	if errors.Is(err, ErrBusy) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	rel()
	return false, nil
}

// HoldCreate takes the one-creation-at-a-time hold (Dir/ops/create.lock),
// waiting up to wait; it returns ErrBusy after that.
func (s Store) HoldCreate(ctx context.Context, wait time.Duration) (release func(), err error) {
	return flockFile(ctx, s.opPath("create"), wait)
}

// HoldReap takes the single-flight hold of a reap (Dir/reap.lock) and fails at
// once with ErrBusy while another reap of this repository holds it. The hold
// dies with its process.
func (s Store) HoldReap() (release func(), err error) {
	return flockFile(context.Background(), filepath.Join(s.Dir, "reap.lock"), 0)
}
