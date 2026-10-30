//go:build unix

package copier

import (
	"io/fs"
	"syscall"
)

// Identity is a file's identity: device, inode and birth time.
type Identity struct {
	Dev, Ino uint64
	Birth    int64
}

func identityOf(path string, fi fs.FileInfo) (Identity, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, false
	}
	return Identity{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Birth: birthNS(path, st)}, true
}
