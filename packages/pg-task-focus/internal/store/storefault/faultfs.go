// Package storefault is a fault-injecting store.FS for tests. It wraps another
// FS (the operating system by default, so use a temporary directory), records
// every call in order, and fails chosen calls with a chosen error, optionally
// after writing only a prefix of the bytes of a Write.
//
// It is shared by the store tests (an external test package, which is how the
// import cycle is avoided) and by the engine tests (engine.Options.FS), so its
// surface is small and stable: New, Inject, Pending, Calls and ResetCalls.
//
// Because it wraps a real file system, a file opened with O_APPEND really
// appends: a write lands at the current end of the file even after a
// Truncate, which is the behaviour the store's rollback depends on. The fault
// FS records the flag so a test can assert the store asked for it.
package storefault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// ErrInjected is the error a Rule returns when it names none.
var ErrInjected = errors.New("storefault: injected fault")

// Op names one operation of store.FS or store.File.
type Op string

// The operations that are recorded and can be failed.
const (
	OpOpenFile   Op = "OpenFile"
	OpStat       Op = "Stat" // both FS.Stat and File.Stat
	OpRemove     Op = "Remove"
	OpMkdirAll   Op = "MkdirAll"
	OpCreateTemp Op = "CreateTemp"
	OpReadAt     Op = "ReadAt"
	OpWrite      Op = "Write"
	OpSync       Op = "Sync"
	OpTruncate   Op = "Truncate"
	OpClose      Op = "Close"
)

// Call is one recorded call. A failed call is recorded too.
type Call struct {
	// Op is the operation.
	Op Op
	// Name is the file: the name the file was opened with for a call on a
	// File; the path argument for an FS call; for CreateTemp, the directory
	// joined with the pattern.
	Name string
	// Flag is the flag argument of OpenFile, and zero for every other call.
	Flag int
	// Size is the number of bytes of a Write or ReadAt, the length argument of
	// a Truncate, and zero for every other call.
	Size int64
}

// Rule makes one call fail. It counts matching calls from the moment it is
// injected, fires on the Nth of them, and is then spent.
type Rule struct {
	// Op is the operation to fail.
	Op Op
	// Name, when not empty, restricts the rule to calls whose Call.Name
	// contains it.
	Name string
	// Nth is the 1-based number of the matching call that fails; zero means
	// the first.
	Nth int
	// Err is returned by the failed call; nil means ErrInjected.
	Err error
	// Partial applies to OpWrite only: the failed Write first writes this many
	// bytes (at most the length of the write) and reports them in its count.
	Partial int
}

// FS is the fault-injecting store.FS. It is safe for concurrent use.
type FS struct {
	inner store.FS

	mu    sync.Mutex
	rules []*pending
	calls []Call
}

type pending struct {
	Rule
	seen  int
	fired bool
}

// New returns a fault FS over inner; a nil inner means store.OS().
func New(inner store.FS) *FS {
	if inner == nil {
		inner = store.OS()
	}
	return &FS{inner: inner}
}

// Inject adds a rule. Rules are independent: each counts the calls that match
// it and fires once.
func (f *FS) Inject(r Rule) {
	if r.Nth < 1 {
		r.Nth = 1
	}
	if r.Err == nil {
		r.Err = ErrInjected
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, &pending{Rule: r})
}

// Pending returns the number of injected rules that have not fired yet, so a
// test can prove its fault was reached.
func (f *FS) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.rules {
		if !r.fired {
			n++
		}
	}
	return n
}

// Calls returns a copy of the recorded calls, oldest first.
func (f *FS) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// ResetCalls forgets the recorded calls; rules and their counts are kept.
func (f *FS) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// intercept records c and returns the rule that fires on it, if any. The
// first rule in injection order that fires wins; every matching rule counts
// the call.
func (f *FS) intercept(c Call) (Rule, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	var hit *Rule
	for _, r := range f.rules {
		if r.fired || r.Op != c.Op || !strings.Contains(c.Name, r.Name) {
			continue
		}
		r.seen++
		if r.seen == r.Nth {
			r.fired = true
			if hit == nil {
				rule := r.Rule
				hit = &rule
			}
		}
	}
	if hit == nil {
		return Rule{}, false
	}
	return *hit, true
}

// OpenFile implements store.FS.
func (f *FS) OpenFile(name string, flag int, perm os.FileMode) (store.File, error) {
	if r, hit := f.intercept(Call{Op: OpOpenFile, Name: name, Flag: flag}); hit {
		return nil, r.Err
	}
	file, err := f.inner.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &faultFile{fs: f, inner: file, name: name}, nil
}

// Stat implements store.FS.
func (f *FS) Stat(name string) (os.FileInfo, error) {
	if r, hit := f.intercept(Call{Op: OpStat, Name: name}); hit {
		return nil, r.Err
	}
	return f.inner.Stat(name)
}

// Remove implements store.FS.
func (f *FS) Remove(name string) error {
	if r, hit := f.intercept(Call{Op: OpRemove, Name: name}); hit {
		return r.Err
	}
	return f.inner.Remove(name)
}

// MkdirAll implements store.FS.
func (f *FS) MkdirAll(path string, perm os.FileMode) error {
	if r, hit := f.intercept(Call{Op: OpMkdirAll, Name: path}); hit {
		return r.Err
	}
	return f.inner.MkdirAll(path, perm)
}

// CreateTemp implements store.FS.
func (f *FS) CreateTemp(dir, pattern string) (store.File, string, error) {
	if r, hit := f.intercept(Call{Op: OpCreateTemp, Name: filepath.Join(dir, pattern)}); hit {
		return nil, "", r.Err
	}
	file, name, err := f.inner.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	return &faultFile{fs: f, inner: file, name: name}, name, nil
}

// faultFile is an open file of the fault FS.
type faultFile struct {
	fs    *FS
	inner store.File
	name  string
}

func (f *faultFile) ReadAt(p []byte, off int64) (int, error) {
	if r, hit := f.fs.intercept(Call{Op: OpReadAt, Name: f.name, Size: int64(len(p))}); hit {
		return 0, r.Err
	}
	return f.inner.ReadAt(p, off)
}

func (f *faultFile) Write(p []byte) (int, error) {
	r, hit := f.fs.intercept(Call{Op: OpWrite, Name: f.name, Size: int64(len(p))})
	if !hit {
		return f.inner.Write(p)
	}
	n := min(max(r.Partial, 0), len(p))
	if n > 0 {
		written, err := f.inner.Write(p[:n])
		if err != nil {
			return written, err
		}
	}
	return n, r.Err
}

func (f *faultFile) Sync() error {
	if r, hit := f.fs.intercept(Call{Op: OpSync, Name: f.name}); hit {
		return r.Err
	}
	return f.inner.Sync()
}

func (f *faultFile) Truncate(size int64) error {
	if r, hit := f.fs.intercept(Call{Op: OpTruncate, Name: f.name, Size: size}); hit {
		return r.Err
	}
	return f.inner.Truncate(size)
}

func (f *faultFile) Stat() (os.FileInfo, error) {
	if r, hit := f.fs.intercept(Call{Op: OpStat, Name: f.name}); hit {
		return nil, r.Err
	}
	return f.inner.Stat()
}

// Close closes the underlying file even when a rule makes it report an
// error, as a real failing close does: the descriptor is gone either way.
func (f *faultFile) Close() error {
	r, hit := f.fs.intercept(Call{Op: OpClose, Name: f.name})
	err := f.inner.Close()
	if hit {
		return r.Err
	}
	return err
}
