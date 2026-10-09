//go:build unix

package stranded

import (
	"io/fs"
	"syscall"
)

// fileID identifies a file independent of its path.
type fileID struct{ dev, ino uint64 }

// idOf returns the device and inode behind info. A FileInfo that carries no
// Stat_t (never the case for a real os.Stat) gets the zero id, which at worst
// makes every such file share one cache entry and be re-read from the start.
func idOf(info fs.FileInfo) fileID {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)} //nolint:unconvert // Dev and Ino widths differ per platform
}
