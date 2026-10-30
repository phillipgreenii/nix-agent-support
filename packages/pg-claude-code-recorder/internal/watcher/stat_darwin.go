//go:build darwin

package watcher

import (
	"io/fs"
	"syscall"
	"time"
)

func infoFromStat(path string, fi fs.FileInfo) FileInfo {
	out := FileInfo{Path: path, Size: fi.Size(), ModTime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		out.Dev = uint64(uint32(st.Dev))
		out.Ino = st.Ino
		out.Birth = time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return out
}
