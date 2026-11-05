package serve

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// FreeBytes returns the bytes available to the user on the file system holding
// path.
func FreeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // block counts fit int64
}

// DirBytes sums the sizes of the regular files under dir (errors on single
// entries are skipped: the state directory is changing while it is read).
func DirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total
}

// stateFiles are the files of the state directory outside copies/ whose sizes
// the database-derived figure adds (five stats per refresh).
var stateFiles = []string{
	store.DBName, store.DBName + "-wal", store.DBName + "-shm",
	store.JournalName, checkpointName,
}

// StateUsedBytes is the database-derived size of the state directory, computed
// with no directory walk: the copies the database lists (Reader.CopyBytes) plus
// the sizes of index.db, its -wal and -shm, the journal and the watcher
// checkpoint. It is approximate on purpose: archives, manifests, the doctor
// cursor and orphan files are NOT counted, and it is not a same-instant
// reading. The quarantine lives beside the Claude originals, outside the state
// directory and outside every table summed here, so the caller adds
// quarantine_bytes without double counting.
//
// The private (physical, unshared) size of copies/ cannot be read from the
// database; the clone_private_* series stay the only measure of it.
func StateUsedBytes(r *store.Reader, stateDir string) (int64, error) {
	total, err := r.CopyBytes()
	if err != nil {
		return 0, err
	}
	for _, n := range stateFiles {
		if fi, err := os.Stat(filepath.Join(stateDir, n)); err == nil {
			total += fi.Size()
		}
	}
	return total, nil
}
