package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

const (
	// logName is the log, in the data directory.
	logName = "events.jsonl"
	// lockName is the file the directory lock is taken on.
	lockName = "events.jsonl.lock"
	// dataDirName is the directory the data home holds.
	dataDirName = "pg-task-focus"

	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// ErrLocked is returned by Open when another store holds the data directory
// (INV-LOG-20).
var ErrLocked = errors.New("the data directory is in use by another pg-task-focus instance")

// Options configures Open.
type Options struct {
	// Dir is the data directory, created with mode 0700 when absent.
	Dir string
	// FS is the file system the log is read and written through; nil means
	// the operating system. The directory lock always uses the operating
	// system.
	FS FS
	// Now stamps the recovery sidecar and the instant read-only mode began;
	// nil means time.Now.
	Now func() time.Time
}

// Recovery reports what Open did to the end of the log (INV-LOG-10), or, for
// Check, what the next Open would do. The zero value means nothing.
type Recovery struct {
	// TornTail is set when the final line was never acknowledged: no
	// terminating newline, blank, or not a valid event.
	TornTail bool
	// UncommittedBatches is 1 when the log ended in a batch that was never
	// closed by its batch.committed, and 0 otherwise.
	UncommittedBatches int
	// TruncatedBytes is the number of bytes cut from the end of the log.
	TruncatedBytes int64
	// Sidecar is the path of the file the cut bytes were copied to; empty
	// when nothing was cut and for Check, which copies nothing.
	Sidecar string
}

// Store is the open event log: the directory lock, the log opened for append,
// and the read-only state. It is safe for concurrent use.
type Store struct {
	fs   FS
	now  func() time.Time
	dir  string
	path string

	lock *os.File // the directory lock; closing it releases the claim

	// size is the length of the committed log. Only the writer changes it
	// (under mu); Size may read it from any goroutine.
	size atomic.Int64

	// mu serializes everything that touches the log file: Append and Close.
	mu  sync.Mutex
	log File

	// state guards closed and the read-only state, which Health, Writable and
	// the failing Append all touch, without waiting for a slow disk.
	state  sync.Mutex
	closed bool
	health Health
}

// Open takes the directory lock, opens the log for append, recovers the end of
// the log (INV-LOG-9, INV-LOG-10) and returns the committed events in log
// order. A log that is corrupt, or written in an unknown version, is returned
// as a *CorruptError or *UnknownVersionError and is not modified (INV-LOG-2,
// INV-LOG-11). The directory is created if absent, and so is an empty log.
func Open(opts Options) (*Store, []event.Event, Recovery, error) {
	if opts.Dir == "" {
		return nil, nil, Recovery{}, errors.New("the event store needs a data directory")
	}
	fsys := opts.FS
	if fsys == nil {
		fsys = OS()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if err := fsys.MkdirAll(opts.Dir, dirMode); err != nil {
		return nil, nil, Recovery{}, fmt.Errorf("creating the data directory %s: %w", opts.Dir, err)
	}
	lock, err := lockDir(filepath.Join(opts.Dir, lockName))
	if err != nil {
		return nil, nil, Recovery{}, err
	}
	s := &Store{fs: fsys, now: now, dir: opts.Dir, path: filepath.Join(opts.Dir, logName), lock: lock}
	events, rec, err := s.load()
	if err != nil {
		_ = lock.Close()
		return nil, nil, Recovery{}, err
	}
	return s, events, rec, nil
}

// load opens the log, reads it and recovers its tail.
func (s *Store) load() ([]event.Event, Recovery, error) {
	log, err := s.fs.OpenFile(s.path, os.O_RDWR|os.O_APPEND|os.O_CREATE, fileMode)
	if err != nil {
		return nil, Recovery{}, fmt.Errorf("opening the event log %s: %w", s.path, err)
	}
	events, rec, err := s.read(log)
	if err != nil {
		_ = log.Close()
		return nil, Recovery{}, err
	}
	s.log = log
	return events, rec, nil
}

func (s *Store) read(log File) ([]event.Event, Recovery, error) {
	info, err := log.Stat()
	if err != nil {
		return nil, Recovery{}, fmt.Errorf("reading the size of the event log: %w", err)
	}
	events, end, rep, err := scan(io.NewSectionReader(log, 0, info.Size()))
	if err != nil {
		return nil, Recovery{}, err
	}
	rec := wouldRecover(rep, end)
	if rec != (Recovery{}) {
		rec.Sidecar, err = recoverTail(s.fs, log, s.dir, s.now(), end, rep.Size)
		if err != nil {
			return nil, Recovery{}, fmt.Errorf("recovering the end of the event log: %w", err)
		}
	}
	s.size.Store(end)
	return events, rec, nil
}

// Size is the length in bytes of the committed log.
func (s *Store) Size() int64 { return s.size.Load() }

// Close releases the log and the directory claim. It is safe to call twice.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Lock()
	already := s.closed
	s.closed = true
	s.state.Unlock()
	if already {
		return nil
	}
	err := s.log.Close()
	// Closing the lock file releases the kernel lock: nothing else to undo,
	// and a lock left behind by a crash is released the same way.
	return errors.Join(err, s.lock.Close())
}

// lockDir takes the exclusive claim on the data directory (INV-LOG-20). The
// claim lives in a real file descriptor, so it needs no FS seam and dies with
// the process however it dies; the file it is taken on is left in place, and
// a file with no holder never blocks.
func lockDir(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return nil, fmt.Errorf("opening the lock file %s: %w", path, err)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, filepath.Dir(path))
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return f, nil
}

// flockExclusive is the only call of flock: a non-blocking exclusive lock.
func flockExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// DefaultDir returns the data directory: pg-task-focus in
// $XDG_DATA_HOME when that is an absolute path, otherwise in
// $HOME/.local/share. With neither it returns the empty string, never a
// relative path.
func DefaultDir(getenv func(string) string) string {
	if xdg := getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, dataDirName)
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "share", dataDirName)
	}
	return ""
}
