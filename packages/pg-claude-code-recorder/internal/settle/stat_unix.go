//go:build unix

package settle

import (
	"io/fs"
	"os"
	"syscall"
)

// fileStat is what removal needs to know about a file without following links.
type fileStat struct {
	Dev, Ino, Nlink uint64
	Size            int64
	MtimeNS         int64
	Mode            fs.FileMode
}

func lstat(path string) (fileStat, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return fileStat{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{}, syscall.ENOTSUP
	}
	return fileStat{
		Dev: uint64(st.Dev), Ino: uint64(st.Ino), Nlink: uint64(st.Nlink),
		Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano(), Mode: fi.Mode(),
	}, nil
}

// syscallEXDEV is the error a rename across devices returns.
var syscallEXDEV = syscall.EXDEV
