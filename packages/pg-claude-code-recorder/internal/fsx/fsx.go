// Package fsx is the file-system seam for the recorder's state directory. The
// copier, journal and store write copies, sidecars and the journal through an
// FS so a Recorder can assert the order of append, fsync and commit.
package fsx

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// File is the subset of *os.File the recorder uses.
type File interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Closer
	Sync() error
	Truncate(size int64) error
	Stat() (fs.FileInfo, error)
	Name() string
}

// FS is the state-directory file system.
type FS interface {
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Stat(name string) (fs.FileInfo, error)
	MkdirAll(path string, perm fs.FileMode) error
	ReadDir(name string) ([]fs.DirEntry, error)
	// SyncDir fsyncs a directory so a rename or create inside it is durable.
	SyncDir(dir string) error
}

// OS is the production FS. Its appends use plain fsync: they are weaker than
// the settle and removal path, which flushes with F_FULLFSYNC.
type OS struct{}

// OpenFile opens a file.
func (OS) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}

// Rename renames a file.
func (OS) Rename(a, b string) error { return os.Rename(a, b) }

// Remove removes a file.
func (OS) Remove(name string) error { return os.Remove(name) }

// Stat stats a file.
func (OS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

// MkdirAll creates a directory tree.
func (OS) MkdirAll(p string, perm fs.FileMode) error { return os.MkdirAll(p, perm) }

// ReadDir lists a directory.
func (OS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// SyncDir fsyncs the directory.
func (OS) SyncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
