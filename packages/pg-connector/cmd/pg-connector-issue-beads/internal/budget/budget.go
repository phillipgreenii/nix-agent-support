// Package budget is the cross-process counting semaphore of the beads
// backend's bd budget (design 4.4, INV-ICD-9 part a).
//
// The backend is one process per call and has no daemon, so the semaphore is
// MaxConcurrent slot files in a state directory. A call takes one slot with
// flock and hands the slot's open file to the bd child as an extra file
// (exec.Cmd.ExtraFiles). flock locks belong to the open file description, and
// the child's copy of the descriptor shares it, so the slot stays held until
// the CHILD exits even when the backend that took it is killed at the
// umbrella's call deadline while bd keeps running. Release therefore only
// closes the backend's own descriptor; it never issues LOCK_UN, which would
// release the lock for the child's copy as well.
//
// Reads may take only the lowest MaxConcurrent-WriteReserve slots, so the
// remaining slots are reserved for writes. When every eligible slot is taken
// Acquire waits up to WaitBound and then returns ErrDenied. When the state
// directory cannot be used the budget fails open: Acquire returns a usable
// slot with a nil File and FailedOpen true, never an error.
package budget

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// ErrDenied is returned by Acquire when no eligible slot became free within
// the wait bound. The caller maps it to the wire error "unavailable"
// (retryable).
var ErrDenied = errors.New("budget: no bd slot free within the wait bound")

// pollInterval is how long Acquire sleeps between non-blocking lock attempts
// while every eligible slot is taken.
const pollInterval = 10 * time.Millisecond

// Config configures a Budget.
type Config struct {
	// MaxConcurrent is the number of slot files (bd processes in flight).
	MaxConcurrent int
	// MaxPerMinute is the sliding-window cap on bd starts per minute. It is
	// carried here for the runner wiring; the semaphore does not use it.
	MaxPerMinute int
	// WaitBound is how long Acquire waits for a free slot before ErrDenied.
	// Zero means no wait.
	WaitBound time.Duration
	// WriteReserve is the number of slots only writes may take.
	WriteReserve int
	// StateDir, when non-empty, is used verbatim as the slot directory.
	StateDir string
	// Group names a semaphore shared by backends that serve one dolt server.
	// It is joined to the default state directory; empty means "default".
	Group string
}

// Budget hands out slots of one semaphore.
type Budget struct {
	cfg Config
	dir string
}

// New validates cfg and returns a Budget. It does not touch the filesystem.
func New(cfg Config) (*Budget, error) {
	if cfg.MaxConcurrent < 1 {
		return nil, fmt.Errorf("budget: MaxConcurrent must be at least 1, got %d", cfg.MaxConcurrent)
	}
	if cfg.WriteReserve < 0 || cfg.WriteReserve >= cfg.MaxConcurrent {
		return nil, fmt.Errorf("budget: WriteReserve must be in [0, MaxConcurrent), got %d with MaxConcurrent %d",
			cfg.WriteReserve, cfg.MaxConcurrent)
	}
	dir := cfg.StateDir
	if dir == "" {
		dir = DefaultStateDir(cfg.Group)
	}
	return &Budget{cfg: cfg, dir: dir}, nil
}

// Dir is the slot directory this budget uses.
func (b *Budget) Dir() string { return b.dir }

// DefaultStateDir is the deterministic slot directory for a group. It depends
// only on the account (the passwd entry of the current uid), never on
// $XDG_STATE_HOME or $HOME, which a launchd job and an interactive shell
// resolve differently. When the passwd entry cannot be read it falls back to a
// per-uid directory under /tmp.
func DefaultStateDir(group string) string {
	if group == "" {
		group = "default"
	}
	uid := strconv.Itoa(os.Getuid())
	if u, err := user.LookupId(uid); err == nil && u.HomeDir != "" {
		return filepath.Join(u.HomeDir, ".local", "state", "pg-connector-issue-beads", "budget", group)
	}
	return filepath.Join("/tmp", "pg-connector-issue-beads-"+uid, "budget", group)
}

// Slot is one held (or failed-open) budget slot.
type Slot struct {
	mu         sync.Mutex
	file       *os.File
	waited     time.Duration
	failedOpen bool
	failErr    error
}

// File is the slot's open file, to be passed to the bd child in
// exec.Cmd.ExtraFiles. It is nil when the budget failed open.
func (s *Slot) File() *os.File { return s.file }

// Waited is how long Acquire waited for the slot; zero on a fail-open slot.
func (s *Slot) Waited() time.Duration { return s.waited }

// FailedOpen is true exactly when File is nil because the state directory
// could not be used.
func (s *Slot) FailedOpen() bool { return s.failedOpen }

// FailOpenErr is the reason the budget failed open, nil otherwise.
func (s *Slot) FailOpenErr() error { return s.failErr }

// Release closes the backend's own descriptor of the slot. It is idempotent
// and a no-op on a fail-open slot. If a child still holds the file as an
// extra file the lock stays held until that child exits.
func (s *Slot) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.failedOpen {
		return
	}
	f := s.file
	s.file = nil
	_ = f.Close()
}

// Acquire takes a free slot. A read may take only the lowest
// MaxConcurrent-WriteReserve slots; a write may take any. When all eligible
// slots are taken it waits up to WaitBound and then returns ErrDenied, or
// ctx.Err() when the context ends first. An unusable state directory yields
// a fail-open slot and no error.
func (b *Budget) Acquire(ctx context.Context, write bool) (*Slot, error) {
	eligible := b.cfg.MaxConcurrent
	if !write {
		eligible -= b.cfg.WriteReserve
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return failOpen(err), nil
	}
	files := make([]*os.File, 0, eligible)
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for i := 0; i < eligible; i++ {
		f, err := os.OpenFile(filepath.Join(b.dir, fmt.Sprintf("slot-%d", i)), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			closeAll()
			return failOpen(err), nil
		}
		files = append(files, f)
	}

	start := time.Now()
	deadline := start.Add(b.cfg.WaitBound)
	for {
		if err := ctx.Err(); err != nil {
			closeAll()
			return nil, err
		}
		for i, f := range files {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				for j, other := range files {
					if j != i {
						_ = other.Close()
					}
				}
				return &Slot{file: f, waited: time.Since(start)}, nil
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
				closeAll()
				return failOpen(err), nil
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			closeAll()
			return nil, ErrDenied
		}
		wait := pollInterval
		if remaining < wait {
			wait = remaining
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			closeAll()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func failOpen(err error) *Slot { return &Slot{failedOpen: true, failErr: err} }
