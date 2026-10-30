package fsx

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
)

// Event is one recorded operation.
type Event struct {
	Op   string // write, sync, truncate, rename, remove, syncdir, create, mark
	Path string // for rename, "old->new"
}

func (e Event) String() string { return e.Op + " " + e.Path }

// Recorder is the named recording file-system wrapper. It wraps an FS and logs
// every mutating operation in order. The store's commit hook calls Mark("commit")
// so the SQLite commit appears in the same sequence.
type Recorder struct {
	Inner FS

	mu     sync.Mutex
	events []Event
}

// NewRecorder wraps inner (OS when nil).
func NewRecorder(inner FS) *Recorder {
	if inner == nil {
		inner = OS{}
	}
	return &Recorder{Inner: inner}
}

func (r *Recorder) add(op, path string) {
	r.mu.Lock()
	r.events = append(r.events, Event{Op: op, Path: path})
	r.mu.Unlock()
}

// Mark records a non-file event such as the SQLite commit.
func (r *Recorder) Mark(op string) { r.add("mark", op) }

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// Reset clears the recorded events.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

// OpenFile opens a file; writes through the handle are recorded.
func (r *Recorder) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	_, statErr := r.Inner.Stat(name)
	f, err := r.Inner.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	if flag&os.O_CREATE != 0 && os.IsNotExist(statErr) {
		r.add("create", name)
	}
	return &recFile{File: f, r: r}, nil
}

// Rename records and performs a rename.
func (r *Recorder) Rename(a, b string) error {
	r.add("rename", a+"->"+b)
	return r.Inner.Rename(a, b)
}

// Remove records and performs a removal.
func (r *Recorder) Remove(name string) error {
	r.add("remove", name)
	return r.Inner.Remove(name)
}

// Stat stats a file.
func (r *Recorder) Stat(name string) (fs.FileInfo, error) { return r.Inner.Stat(name) }

// MkdirAll creates a directory tree.
func (r *Recorder) MkdirAll(p string, perm fs.FileMode) error { return r.Inner.MkdirAll(p, perm) }

// ReadDir lists a directory.
func (r *Recorder) ReadDir(name string) ([]fs.DirEntry, error) { return r.Inner.ReadDir(name) }

// SyncDir records and performs a directory fsync.
func (r *Recorder) SyncDir(dir string) error {
	r.add("syncdir", dir)
	return r.Inner.SyncDir(dir)
}

type recFile struct {
	File
	r *Recorder
}

func (f *recFile) Write(p []byte) (int, error) {
	f.r.add("write", f.Name())
	return f.File.Write(p)
}

func (f *recFile) WriteAt(p []byte, off int64) (int, error) {
	f.r.add("write", f.Name())
	return f.File.WriteAt(p, off)
}

func (f *recFile) Sync() error {
	f.r.add("sync", f.Name())
	return f.File.Sync()
}

func (f *recFile) Truncate(n int64) error {
	f.r.add("truncate", f.Name())
	return f.File.Truncate(n)
}

// Step selects one event in an expected order: Op and a substring of Path.
type Step struct{ Op, PathContains string }

func (s Step) matches(e Event) bool {
	return e.Op == s.Op && strings.Contains(e.Path, s.PathContains)
}

// AssertOrder checks that the steps occur in events as an ordered subsequence.
// It returns a descriptive error naming the first missing step.
func AssertOrder(events []Event, steps ...Step) error {
	i := 0
	for _, e := range events {
		if i < len(steps) && steps[i].matches(e) {
			i++
		}
	}
	if i < len(steps) {
		return fmt.Errorf("order violated: step %d (%s %q) never follows the previous steps; events: %v",
			i, steps[i].Op, steps[i].PathContains, events)
	}
	return nil
}

// CopierAppendOrder is the order the copier MUST keep for an append to an
// existing copy: write the bytes, fsync the copy, then commit the offset.
func CopierAppendOrder(copyPath string) []Step {
	return []Step{{"write", copyPath}, {"sync", copyPath}, {"mark", "commit"}}
}

// CopierNewCopyOrder is the order for a copy created by the copier: write the
// temporary file, fsync it, rename it into place, fsync the directory, commit.
func CopierNewCopyOrder(tmpPath, dir string) []Step {
	return []Step{{"write", tmpPath}, {"sync", tmpPath}, {"rename", tmpPath}, {"syncdir", dir}, {"mark", "commit"}}
}
