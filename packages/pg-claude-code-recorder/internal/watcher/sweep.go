package watcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileInfo is what a sweep and a per-event lstat report about one file. Device,
// inode and birth time are the identity the copier uses to decide that a
// missing path is a deletion only when no file with that identity exists
// elsewhere. Birth is the zero time where the platform does not report it.
type FileInfo struct {
	Path    string
	Dev     uint64
	Ino     uint64
	Birth   time.Time
	Size    int64
	ModTime time.Time
}

// dirReadHook, when set (tests only), is told every directory a sweep reads, so
// a test can prove the walker never opened a directory outside the allowlist.
var dirReadHook func(dir string)

// walkSweep lists every regular file under root with a plain directory walk.
// descend, when non-nil, runs on every subdirectory before it is read; false
// skips the whole subtree (the root itself is always read).
// It is the Linux sweep and the fallback where getattrlistbulk is unavailable.
// Entries that vanish mid-walk are skipped; symlinks are not followed.
func walkSweep(root string, descend func(dir string) bool) ([]FileInfo, error) {
	var out []FileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path != root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if path != root && descend != nil && !descend(path) {
				return filepath.SkipDir
			}
			if dirReadHook != nil {
				dirReadHook(path)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		out = append(out, infoFromStat(path, fi))
		return nil
	})
	return out, err
}

func lstatInfo(path string) (FileInfo, bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, false, err
	}
	if !fi.Mode().IsRegular() {
		return FileInfo{}, false, nil
	}
	return infoFromStat(path, fi), true, nil
}
