package copier

import (
	"io/fs"
	"os"
	"sync/atomic"
	"syscall"
)

// SrcFS is the source-side file system seam: how the copier looks at the files
// under the Claude directory. The state-directory file system (Config.FS)
// wraps only the copy side, so a test that counts or fakes source-side calls
// goes through this one. nil selects the real file system.
type SrcFS interface {
	Lstat(path string) (fs.FileInfo, error)
	// OpenNoFollow opens path read-only and refuses to follow a symlink.
	OpenNoFollow(path string) (*os.File, error)
}

type osSrcFS struct{}

func (osSrcFS) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }
func (osSrcFS) OpenNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// CountingSrcFS wraps a SrcFS (the real one when nil) and counts its calls. It
// is the test implementation that shows a sweep with nothing missing makes no
// source-side lstat.
type CountingSrcFS struct {
	Inner  SrcFS
	lstats atomic.Int64
	opens  atomic.Int64
}

func (c *CountingSrcFS) inner() SrcFS {
	if c.Inner != nil {
		return c.Inner
	}
	return osSrcFS{}
}

// Lstat counts and forwards.
func (c *CountingSrcFS) Lstat(path string) (fs.FileInfo, error) {
	c.lstats.Add(1)
	return c.inner().Lstat(path)
}

// OpenNoFollow counts and forwards.
func (c *CountingSrcFS) OpenNoFollow(path string) (*os.File, error) {
	c.opens.Add(1)
	return c.inner().OpenNoFollow(path)
}

// Lstats is the number of Lstat calls so far.
func (c *CountingSrcFS) Lstats() int64 { return c.lstats.Load() }

// Opens is the number of OpenNoFollow calls so far.
func (c *CountingSrcFS) Opens() int64 { return c.opens.Load() }

// Reset zeroes both counts.
func (c *CountingSrcFS) Reset() { c.lstats.Store(0); c.opens.Store(0) }

func (c *Copier) lstat(path string) (fs.FileInfo, error) { return c.src.Lstat(path) }

func (c *Copier) openNoFollow(path string) (*os.File, error) { return c.src.OpenNoFollow(path) }
