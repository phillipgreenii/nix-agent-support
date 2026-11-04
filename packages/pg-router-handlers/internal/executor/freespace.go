package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LowFreeSpaceError is the pre-creation refusal of INV-CCH-8: the volume that
// would hold a new per-bead worktree has less free space than the configured
// floor (config.Config.MinFreeBytes). It satisfies errors.Is(err, ErrLowDisk),
// so it takes the same busy decline (reason low-disk) as a worktree add that
// ran out of room, but nothing was created and nothing was written.
type LowFreeSpaceError struct {
	Path  string // directory whose volume was measured
	Free  uint64 // bytes available to unprivileged writers
	Floor uint64 // the configured floor
}

func (e *LowFreeSpaceError) Error() string {
	return fmt.Sprintf("worktree: low disk space: %d bytes free on the volume of %s, below the floor of %d bytes; no worktree created", e.Free, e.Path, e.Floor)
}

// Is makes the error match the existing low-disk sentinel.
func (e *LowFreeSpaceError) Is(target error) bool { return target == ErrLowDisk }

// statfsFree reports the bytes available to unprivileged writers on the volume
// holding path. A path that does not exist yet is measured through its nearest
// existing ancestor (the worktree directory is created lazily).
func statfsFree(path string) (uint64, error) {
	p := path
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(p, &st)
		if err == nil {
			return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec // block counts are non-negative
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return 0, err
		}
		p = parent
	}
}

func (d Deps) freeBytes(path string) (uint64, error) {
	if d.FreeBytes != nil {
		return d.FreeBytes(path)
	}
	return statfsFree(path)
}

// checkFreeSpace is the floor check made before a new per-bead worktree is
// created. It returns a *LowFreeSpaceError when free space is below the floor.
// A disabled floor (<= 0), an unset worktree directory, or a measurement
// failure passes: a free-space guard must not itself stop dispatching.
func (d Deps) checkFreeSpace() (free uint64, err error) {
	floor := d.Cfg.MinFreeBytes
	dir := d.Cfg.WorktreeDir
	if floor <= 0 || dir == "" {
		return 0, nil
	}
	free, mErr := d.freeBytes(dir)
	if mErr != nil {
		return 0, nil
	}
	if free < uint64(floor) {
		return free, &LowFreeSpaceError{Path: dir, Free: free, Floor: uint64(floor)}
	}
	return free, nil
}
