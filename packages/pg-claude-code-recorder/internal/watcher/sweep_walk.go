//go:build !(darwin && cgo)

package watcher

func platformSweep(root string) ([]FileInfo, error) { return walkSweep(root) }
