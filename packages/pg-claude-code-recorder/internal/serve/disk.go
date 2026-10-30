package serve

import (
	"io/fs"
	"path/filepath"
	"syscall"
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
