//go:build linux

package copier

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// SystemClone is the production ClonePolicy on Linux: FICLONE where the file
// system supports it.
type SystemClone struct{}

// Probe checks the same st_dev; FICLONE support is learned from the first clone
// (EOPNOTSUPP and EXDEV select byte mode per file).
func (SystemClone) Probe(stateDir, claudeDir string) (bool, string) {
	if !sameDevice(stateDir, claudeDir) {
		return false, "different volume"
	}
	return true, "same volume; FICLONE tried per file"
}

// Clone clones src to dst with FICLONE.
func (SystemClone) Clone(src, dst string) error {
	in, err := os.OpenFile(src, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// PrivateSize is not reported on Linux.
func (SystemClone) PrivateSize(string) (int64, error) { return 0, errors.ErrUnsupported }
