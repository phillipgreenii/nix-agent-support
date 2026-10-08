package store

import (
	"io"
	"os"
	"path/filepath"
)

// FS is the file system the store works through. It is a seam for tests: the
// production implementation (OS) is the operating system, and the
// storefault package wraps any FS to fail chosen calls. The directory lock
// does NOT go through it (see lockDir): a lock needs a real file descriptor.
type FS interface {
	// OpenFile opens a file like os.OpenFile.
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	// Stat describes a file like os.Stat.
	Stat(name string) (os.FileInfo, error)
	// Remove deletes a file like os.Remove.
	Remove(name string) error
	// MkdirAll creates a directory and its parents like os.MkdirAll.
	MkdirAll(path string, perm os.FileMode) error
	// CreateTemp creates a new file in dir like os.CreateTemp and returns it
	// together with its path, which File does not carry.
	CreateTemp(dir, pattern string) (File, string, error)
}

// File is an open file. It is what the store needs of *os.File and nothing
// more; in particular there is no Fd, because the lock bypasses the seam.
type File interface {
	io.ReaderAt
	io.Writer
	// Sync commits the file's content to stable storage.
	Sync() error
	// Truncate changes the file's size.
	Truncate(size int64) error
	// Stat describes the file.
	Stat() (os.FileInfo, error)
	// Close closes the file.
	Close() error
}

// OS returns the FS backed by the operating system.
func OS() FS { return osFS{} }

type osFS struct{}

func (osFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (osFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

func (osFS) Remove(name string) error { return os.Remove(name) }

func (osFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (osFS) CreateTemp(dir, pattern string) (File, string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	return f, filepath.Join(dir, filepath.Base(f.Name())), nil
}

// syncDir makes the entries of the directory dir durable: a file created or
// renamed in it survives a crash only once the directory itself is synced. The
// directory is opened through fsys, so the fault FS records and can fail the
// call.
func syncDir(fsys FS, dir string) error {
	d, err := fsys.OpenFile(dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
