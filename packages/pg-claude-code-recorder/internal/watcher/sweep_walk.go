//go:build !(darwin && cgo)

package watcher

func platformSweep(root string, descend func(dir string) bool) ([]FileInfo, error) {
	return walkSweep(root, descend)
}
