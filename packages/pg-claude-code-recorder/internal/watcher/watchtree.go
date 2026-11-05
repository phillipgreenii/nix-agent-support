package watcher

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// dirFilterSetter is implemented by a backend that must walk directories to
// place its watches (inotify). The watcher hands it the same directory
// predicate the sweep uses, so a backend never watches a tree the recorder
// never ingests.
type dirFilterSetter interface {
	SetDirFilter(descend func(dir string) bool)
}

// walkWatchTree calls addWatch for root and for every directory below it that
// descend accepts, pruning a rejected directory before it is read. It is the
// OS-neutral traversal of the inotify backend (the Linux file keeps only the
// syscalls), so it is unit-tested on every platform.
//
// checkRoot applies descend to root itself: false for a configured root (it is
// always watched, because history.jsonl and new top-level directories appear in
// it), true for a directory a create event reported. When emit is non-nil it
// is called for each regular file found (a new directory may have been filled
// before its watch existed). addWatch returning an error aborts the walk,
// except fs.ErrNotExist, which skips that directory.
func walkWatchTree(root string, descend func(dir string) bool, checkRoot bool, addWatch func(dir string) error, emit func(path string)) error {
	if checkRoot && descend != nil && !descend(root) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if path != root && descend != nil && !descend(path) {
				return filepath.SkipDir
			}
			if err := addWatch(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return filepath.SkipDir
				}
				return err
			}
			noteDirRead(path)
			return nil
		}
		if emit != nil && d.Type().IsRegular() {
			emit(path)
		}
		return nil
	})
}
