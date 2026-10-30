//go:build !darwin && !linux

package watcher

import "io/fs"

func infoFromStat(path string, fi fs.FileInfo) FileInfo {
	return FileInfo{Path: path, Size: fi.Size(), ModTime: fi.ModTime()}
}
