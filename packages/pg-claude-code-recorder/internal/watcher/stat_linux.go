//go:build linux

package watcher

import (
	"io/fs"
	"syscall"
)

// infoFromStat leaves Birth zero: plain stat has no birth time on Linux.
func infoFromStat(path string, fi fs.FileInfo) FileInfo {
	out := FileInfo{Path: path, Size: fi.Size(), ModTime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		out.Dev = uint64(st.Dev) //nolint:unconvert // width differs per arch
		out.Ino = uint64(st.Ino) //nolint:unconvert
	}
	return out
}
