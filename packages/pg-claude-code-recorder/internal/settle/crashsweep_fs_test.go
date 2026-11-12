//go:build crashsweep

package settle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
)

// Crash injection at every file-system operation (build tag crashsweep, its own
// nix check; not part of the unit tier, V-4).
//
// sweepFS wraps the real file system under the fsx.Recorder. Once armed it
// counts every mutating operation that goes through the fsx seam (a create, a
// write, a sync, a truncate, a rename, a remove, a directory sync, a MkdirAll
// that made a directory), every store commit (the commit hook) and the two steps
// of a clone. After the limit-th operation has taken effect it panics with a
// crashSignal, which stops the process at that point; every later call fails
// with errSweepCrashed, so a deferred cleanup cannot undo or extend the crash.
//
// With power set, sweepFS also keeps a shadow of every file's last synced
// content; dropUnsynced puts it back, which discards every write that no sync
// had made durable (the power-loss variant). Directory entries are not
// modelled: a rename or create that was not followed by a directory sync is
// treated as durable, and a file created and never synced comes back empty.

var errSweepCrashed = errors.New("crashsweep: the process has crashed")

// crashSignal is the panic value of an injected crash.
type crashSignal struct {
	After int
	Op    string
}

type shadowState struct {
	base  []byte // content at the last sync (empty for a file never synced)
	dirty bool
}

type sweepFS struct {
	inner fsx.FS

	mu      sync.Mutex
	armed   bool
	crashed bool
	limit   int // crash after this operation (0 counts only)
	n       int
	ops     []string
	shadow  map[string]*shadowState
}

func newSweepFS() *sweepFS {
	return &sweepFS{inner: fsx.OS{}, shadow: map[string]*shadowState{}}
}

// arm starts counting. Everything written so far counts as durable.
func (s *sweepFS) arm(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed, s.limit, s.n, s.ops = true, limit, 0, nil
	s.shadow = map[string]*shadowState{}
}

func (s *sweepFS) opCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *sweepFS) opLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ops...)
}

func (s *sweepFS) gate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crashed {
		return errSweepCrashed
	}
	return nil
}

// step records one finished operation and crashes when it is the limit-th.
func (s *sweepFS) step(op string) {
	s.mu.Lock()
	if !s.armed || s.crashed {
		s.mu.Unlock()
		return
	}
	s.n++
	s.ops = append(s.ops, op)
	if s.limit > 0 && s.n == s.limit {
		s.crashed = true
		n := s.n
		s.mu.Unlock()
		panic(crashSignal{After: n, Op: op})
	}
	s.mu.Unlock()
}

// markDirty remembers the synced content of path before its first unsynced change.
func (s *sweepFS) markDirty(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.armed {
		return
	}
	if st := s.shadow[path]; st != nil && st.dirty {
		return
	}
	b, _ := os.ReadFile(path)
	s.shadow[path] = &shadowState{base: b, dirty: true}
}

func (s *sweepFS) markNew(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed {
		s.shadow[path] = &shadowState{dirty: true}
	}
}

func (s *sweepFS) markSynced(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shadow, path)
}

// dropUnsynced restores every file with unsynced changes to its last synced content.
func (s *sweepFS) dropUnsynced() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for p, st := range s.shadow {
		if !st.dirty {
			continue
		}
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := os.WriteFile(p, st.base, 0o600); err == nil {
			dropped++
		}
	}
	s.shadow = map[string]*shadowState{}
	return dropped
}

func (s *sweepFS) commit() { s.step("commit") }

func (s *sweepFS) OpenFile(name string, flag int, perm fs.FileMode) (fsx.File, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	_, statErr := s.inner.Stat(name)
	exists := statErr == nil
	if exists && flag&os.O_TRUNC != 0 {
		s.markDirty(name)
	}
	f, err := s.inner.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	sf := &sweepFile{File: f, fs: s}
	switch {
	case !exists && flag&os.O_CREATE != 0:
		s.markNew(name)
		s.step("create " + name)
	case exists && flag&os.O_TRUNC != 0:
		s.step("truncate-open " + name)
	}
	return sf, nil
}

func (s *sweepFS) Rename(a, b string) error {
	if err := s.gate(); err != nil {
		return err
	}
	if err := s.inner.Rename(a, b); err != nil {
		return err
	}
	s.mu.Lock()
	if st, ok := s.shadow[a]; ok {
		s.shadow[b] = st
		delete(s.shadow, a)
	} else {
		delete(s.shadow, b)
	}
	s.mu.Unlock()
	s.step("rename " + a + "->" + b)
	return nil
}

func (s *sweepFS) Remove(name string) error {
	if err := s.gate(); err != nil {
		return err
	}
	if err := s.inner.Remove(name); err != nil {
		return err
	}
	s.markSynced(name)
	s.step("remove " + name)
	return nil
}

func (s *sweepFS) Stat(name string) (fs.FileInfo, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	return s.inner.Stat(name)
}

func (s *sweepFS) MkdirAll(p string, perm fs.FileMode) error {
	if err := s.gate(); err != nil {
		return err
	}
	_, statErr := s.inner.Stat(p)
	if err := s.inner.MkdirAll(p, perm); err != nil {
		return err
	}
	if statErr != nil {
		s.step("mkdir " + p)
	}
	return nil
}

func (s *sweepFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	return s.inner.ReadDir(name)
}

func (s *sweepFS) SyncDir(dir string) error {
	if err := s.gate(); err != nil {
		return err
	}
	if err := s.inner.SyncDir(dir); err != nil {
		return err
	}
	s.step("syncdir " + dir)
	return nil
}

type sweepFile struct {
	fsx.File
	fs *sweepFS
}

func (f *sweepFile) Write(p []byte) (int, error) {
	if err := f.fs.gate(); err != nil {
		return 0, err
	}
	f.fs.markDirty(f.Name())
	n, err := f.File.Write(p)
	if err == nil {
		f.fs.step("write " + f.Name())
	}
	return n, err
}

func (f *sweepFile) WriteAt(p []byte, off int64) (int, error) {
	if err := f.fs.gate(); err != nil {
		return 0, err
	}
	f.fs.markDirty(f.Name())
	n, err := f.File.WriteAt(p, off)
	if err == nil {
		f.fs.step("write " + f.Name())
	}
	return n, err
}

func (f *sweepFile) Truncate(size int64) error {
	if err := f.fs.gate(); err != nil {
		return err
	}
	f.fs.markDirty(f.Name())
	err := f.File.Truncate(size)
	if err == nil {
		f.fs.step("truncate " + f.Name())
	}
	return err
}

func (f *sweepFile) Sync() error {
	if err := f.fs.gate(); err != nil {
		return err
	}
	if err := f.File.Sync(); err != nil {
		return err
	}
	f.fs.markSynced(f.Name())
	f.fs.step("sync " + f.Name())
	return nil
}

// sweepClone is the clone seam with the copy split in two counted steps, so a
// crash can leave a half-written clone behind. The data is unsynced until the
// settle stage flushes it.
type sweepClone struct {
	*fakeClone
	sw *sweepFS
}

func (c *sweepClone) Clone(src, dst string) error {
	if err := c.sw.gate(); err != nil {
		return err
	}
	c.calls++
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	c.sw.markNew(dst)
	half := len(b) / 2
	if _, err := out.Write(b[:half]); err != nil {
		return err
	}
	c.sw.step(fmt.Sprintf("clone-half %s", dst))
	if _, err := out.Write(b[half:]); err != nil {
		return err
	}
	c.sw.step(fmt.Sprintf("clone-done %s", dst))
	return nil
}

// runUntilCrash runs fn and reports whether an injected crash ended it. Any
// other panic propagates.
func runUntilCrash(fn func()) (crashed bool, sig crashSignal) {
	defer func() {
		if r := recover(); r != nil {
			if cs, ok := r.(crashSignal); ok {
				crashed, sig = true, cs
				return
			}
			panic(r)
		}
	}()
	fn()
	return false, crashSignal{}
}
