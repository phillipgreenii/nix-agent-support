package store

import (
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

const (
	// rebuildSizeNum and rebuildSizeDen are the estimated size of the rebuilt
	// index.db relative to the file it replaces: 85 percent, from the design
	// measurement (a version 2 file of 30.65 GB becomes about 25.6 GB).
	rebuildSizeNum = 85
	rebuildSizeDen = 100
	// rebuildWALAllowance is the room for the write-ahead log of the bulk load:
	// the WAL autocheckpoint is 1 GiB (bulkAutocheckpointPages).
	rebuildWALAllowance = int64(JournalSizeLimit)
)

// InsufficientSpaceError is returned by Open when a rebuild would not fit beside
// the file it sets aside. Nothing was moved or deleted.
type InsufficientSpaceError struct {
	Dir                   string
	FreeBytes, NeedBytes  int64
	OldBytes, EstimateNew int64
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf("rebuild refused: %d bytes free in %s, the rebuild needs about %d (the new index.db is estimated at %d bytes from the %d-byte file it replaces, plus %d for its write-ahead log); "+
		"free that space and run the rebuild again, nothing was changed (the old index.db is still in place)",
		e.FreeBytes, e.Dir, e.NeedBytes, e.EstimateNew, e.OldBytes, rebuildWALAllowance)
}

// RebuildSpaceNeeded is the free space a rebuild of an index.db of oldBytes
// requires: the estimated new file plus the allowance for its WAL. The old file
// stays on disk beside the new one (index.db.pre-rebuild).
func RebuildSpaceNeeded(oldBytes int64) (need, estimate int64) {
	estimate = oldBytes * rebuildSizeNum / rebuildSizeDen
	return estimate + rebuildWALAllowance, estimate
}

// DiskFreeBytes returns the bytes available to the user on the file system
// holding path.
func DiskFreeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // block counts fit int64
}

// rebuildPreflight refuses a rebuild whose new file would not fit beside the old
// one. It runs before the old file is moved, so a refusal leaves the state
// directory exactly as it was; it logs the event and counts the refusal.
func (s *Store) rebuildPreflight(dbPath string) error {
	var old int64
	for _, suf := range []string{"", "-wal"} {
		if fi, err := os.Stat(dbPath + suf); err == nil {
			old += fi.Size()
		}
	}
	need, est := RebuildSpaceNeeded(old)
	free := s.freeFn
	if free == nil {
		free = DiskFreeBytes
	}
	got, err := free(s.dir)
	if err != nil {
		// An unreadable free figure must not block a rebuild the operator asked for.
		s.obs.Event(obs.EvRebuildPreflight, "result", "unknown", "err", err.Error())
		return nil
	}
	if got < need {
		s.obs.Reg.Add(obs.MRebuildRefused, nil, 1)
		s.obs.NoteBlocked(obs.OpRebuild, "rebuild preflight refused", strconv.FormatInt(need, 10), strconv.FormatInt(got, 10))
		s.obs.Event(obs.EvRebuildRefused, "free_bytes", strconv.FormatInt(got, 10), "need_bytes", strconv.FormatInt(need, 10),
			"old_index_bytes", strconv.FormatInt(old, 10), "estimated_new_bytes", strconv.FormatInt(est, 10))
		return &InsufficientSpaceError{Dir: s.dir, FreeBytes: got, NeedBytes: need, OldBytes: old, EstimateNew: est}
	}
	s.obs.Event(obs.EvRebuildPreflight, "result", "ok", "free_bytes", strconv.FormatInt(got, 10), "need_bytes", strconv.FormatInt(need, 10))
	return nil
}
