package keeper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// FileKeeperLock is the keeper's singleton lock file in the state directory.
const FileKeeperLock = "keeper.lock"

// ErrAlreadyRunning means another keeper holds keeper.lock.
var ErrAlreadyRunning = errors.New("another keeper already holds keeper.lock")

// InstanceLock is the held flock on keeper.lock. It lives as long as the
// keeper process; the descriptor is close-on-exec so the anchor and caffeinate
// the keeper starts never inherit it (they outlive a restarted keeper and
// would otherwise keep the lock held).
type InstanceLock struct {
	f *os.File
}

// TryLockInstance takes the keeper singleton lock without waiting. A second
// keeper in the same state directory gets ErrAlreadyRunning.
func TryLockInstance(d *lease.Dir) (*InstanceLock, error) {
	path := filepath.Join(d.Path(), FileKeeperLock)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &InstanceLock{f: os.NewFile(uintptr(fd), path)}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *InstanceLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close()
}

// TouchHeartbeat creates keeper.heartbeat when missing and sets its mtime to
// at. The mtime is the contract: the anchor's watchdog tracks changes of it,
// and `status` judges keeper health by its age.
func TouchHeartbeat(d *lease.Dir, at time.Time) error {
	path := filepath.Join(d.Path(), FileHeartbeat)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(path, at, at)
}

// heartbeatMTime is the heartbeat file's mtime, or the zero time when absent.
func heartbeatMTime(d *lease.Dir) time.Time {
	fi, err := os.Lstat(filepath.Join(d.Path(), FileHeartbeat))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// LoadConfig is the keeper's startup configuration load. It follows the
// fallback chain config.Load defines (the file, then config.last-good.toml in
// the state directory, then the built-in defaults) and never fails: the
// returned Config is always usable. The second return is the error text for
// the keeper to log and report as keeper.config_error; it is empty when the
// file parsed or is absent. When the file parsed, its bytes are recorded as
// config.last-good.toml (atomically, mode 0600) so a later bad edit falls
// back to them. warn lists problems that did not affect the result (a failed
// last-good write).
func LoadConfig(d *lease.Dir, path string) (cfg config.Config, errText string, warn []error) {
	lastGood := filepath.Join(d.Path(), lease.FileLastGood)
	cfg, err := config.Load(path, lastGood)
	if err != nil {
		return cfg, err.Error(), nil
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		// Absent file (defaults, nothing to record) or unreadable after a
		// successful parse: no last-good update either way.
		return cfg, "", nil
	}
	if len(data) > config.MaxFileSize {
		return cfg, "", nil
	}
	if _, perr := config.Parse(data); perr != nil {
		return cfg, "", nil // changed between the two reads; keep the old copy
	}
	if werr := d.WriteAtomic(lease.FileLastGood, data); werr != nil {
		warn = append(warn, fmt.Errorf("write %s: %w", lease.FileLastGood, werr))
	}
	return cfg, "", warn
}
