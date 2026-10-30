//go:build linux

package copier

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// birthNS reads the birth time with statx; it is 0 where the file system does
// not report one.
func birthNS(path string, _ *syscall.Stat_t) int64 {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx); err != nil {
		return 0
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return stx.Btime.Sec*1e9 + int64(stx.Btime.Nsec)
}
