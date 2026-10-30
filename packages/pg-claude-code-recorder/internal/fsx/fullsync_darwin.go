//go:build darwin

package fsx

import (
	"os"

	"golang.org/x/sys/unix"
)

// fullSyncFile issues F_FULLFSYNC, which flushes the drive's cache; the plain
// fsync SQLite and os.File.Sync issue on darwin does not.
func fullSyncFile(f *os.File) error {
	if _, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return f.Sync()
	}
	return nil
}
