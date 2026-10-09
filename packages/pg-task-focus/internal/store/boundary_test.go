package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// shortFS is a store.FS over the operating system that misbehaves the way a
// file system may without reporting an error: the first Write to the file
// named write stores only half of the bytes and returns a nil error, and a
// ReadAt of the log at offset readAt returns half of the bytes asked for,
// with readErr.
type shortFS struct {
	write   string // base name; empty: no short write
	readAt  int64  // offset; negative: no short read
	readErr error
}

func (f *shortFS) OpenFile(name string, flag int, perm os.FileMode) (store.File, error) {
	file, err := store.OS().OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &shortFile{inner: file, fs: f, name: filepath.Base(name)}, nil
}

func (f *shortFS) Stat(name string) (os.FileInfo, error) { return store.OS().Stat(name) }
func (f *shortFS) Remove(name string) error              { return store.OS().Remove(name) }
func (f *shortFS) MkdirAll(path string, perm os.FileMode) error {
	return store.OS().MkdirAll(path, perm)
}

func (f *shortFS) CreateTemp(dir, pattern string) (store.File, string, error) {
	return store.OS().CreateTemp(dir, pattern)
}

type shortFile struct {
	inner store.File
	fs    *shortFS
	name  string
	short bool // the short write has happened
}

func (f *shortFile) Write(p []byte) (int, error) {
	if f.name == f.fs.write && !f.short && len(p) > 1 {
		f.short = true
		return f.inner.Write(p[:len(p)/2])
	}
	return f.inner.Write(p)
}

func (f *shortFile) ReadAt(p []byte, off int64) (int, error) {
	if f.name == "events.jsonl" && off == f.fs.readAt && len(p) > 1 {
		n, err := f.inner.ReadAt(p[:len(p)/2], off)
		if err != nil {
			return n, err
		}
		return n, f.fs.readErr
	}
	return f.inner.ReadAt(p, off)
}

func (f *shortFile) Sync() error                { return f.inner.Sync() }
func (f *shortFile) Truncate(size int64) error  { return f.inner.Truncate(size) }
func (f *shortFile) Stat() (os.FileInfo, error) { return f.inner.Stat() }
func (f *shortFile) Close() error               { return f.inner.Close() }

// noShortness is a shortFS that misbehaves nowhere until a test says where.
func noShortness() *shortFS { return &shortFS{readAt: -1} }

// tornLog is the committed log and the same log followed by a torn line.
func tornLog(t *testing.T) (committed, all []byte) {
	t.Helper()
	committed, _ = committedLog(t)
	return committed, append(bytes.Clone(committed), `{"v":1,"id":"01J9Z`...)
}

// INV-LOG-21: a write that stores fewer bytes than it was given, even with no
// error, is a failed write: it is rolled back and the store stays writable.
func TestAppendShortWriteWithoutAnErrorIsRolledBack(t *testing.T) {
	committed, _ := committedLog(t)
	dir := seedLog(t, committed)
	fsys := noShortness()
	fsys.write = "events.jsonl"
	s, _, _ := openStore(t, dir, fsys)

	_, err := s.Append([]event.Event{plainEvent(9)})
	ae := wantAppendError(t, err, store.StageWrite)
	if !errors.Is(ae, io.ErrShortWrite) {
		t.Errorf("Append error %v, want io.ErrShortWrite", ae)
	}
	if !s.Writable() {
		t.Errorf("a rolled-back short write left the store read-only: %+v", s.Health())
	}
	if got := readLog(t, dir); !bytes.Equal(got, committed) {
		t.Errorf("the log after the rollback is %q, want the committed bytes", got)
	}
}

// INV-LOG-10: a sidecar that could not be written whole refuses the start,
// is removed, and leaves the log as it was.
func TestRecoverySidecarShortWriteRefusesToStart(t *testing.T) {
	_, original := tornLog(t)
	dir := seedLog(t, original)
	fsys := noShortness()
	fsys.write = "events.jsonl.recovered-20261008T123045Z-1"

	_, _, _, err := store.Open(store.Options{Dir: dir, FS: fsys, Now: fixedNow})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Open = %v, want io.ErrShortWrite", err)
	}
	if got := sidecars(t, dir); len(got) != 0 {
		t.Errorf("the incomplete sidecar was kept: %v", got)
	}
	if got := readLog(t, dir); !bytes.Equal(got, original) {
		t.Errorf("a failed recovery changed the log")
	}
}

// INV-LOG-10: the unacknowledged bytes are read whole before anything is
// copied; a short read is unexpected end of file, whatever the file system
// said.
func TestRecoveryShortReadRefusesToStart(t *testing.T) {
	for _, readErr := range []error{nil, io.EOF} {
		t.Run(fmt.Sprintf("with %v", readErr), func(t *testing.T) {
			committed, original := tornLog(t)
			dir := seedLog(t, original)
			fsys := noShortness()
			fsys.readAt, fsys.readErr = int64(len(committed)), readErr

			_, _, _, err := store.Open(store.Options{Dir: dir, FS: fsys, Now: fixedNow})
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("Open = %v, want io.ErrUnexpectedEOF", err)
			}
			if got := sidecars(t, dir); len(got) != 0 {
				t.Errorf("a sidecar was written from a short read: %v", got)
			}
			if got := readLog(t, dir); !bytes.Equal(got, original) {
				t.Errorf("a failed recovery changed the log")
			}
		})
	}
}

// With no Options.Now the store stamps a recovery with the system clock.
func TestRecoveryWithoutANowUsesTheSystemClock(t *testing.T) {
	_, original := tornLog(t)
	dir := seedLog(t, original)
	before := time.Now().UTC().Truncate(time.Second)
	s, _, rec, err := store.Open(store.Options{Dir: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	after := time.Now().UTC()
	stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(rec.Sidecar), "events.jsonl.recovered-"), "-1")
	at, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		t.Fatalf("the sidecar %q carries no stamp: %v", rec.Sidecar, err)
	}
	if at.Before(before) || at.After(after) {
		t.Errorf("the sidecar is stamped %s, want between %s and %s", at, before, after)
	}
}

func TestOpenWithNoDirectorySaysSo(t *testing.T) {
	_, _, _, err := store.Open(store.Options{})
	if err == nil || !strings.Contains(err.Error(), "needs a data directory") {
		t.Fatalf("Open with no Dir = %v, want the missing-directory error", err)
	}
}

func TestOpenReportsAFailedStatOfTheLog(t *testing.T) {
	committed, _ := committedLog(t)
	dir := seedLog(t, committed)
	fsys := storefault.New(nil)
	fsys.Inject(storefault.Rule{Op: storefault.OpStat, Name: "events.jsonl"})
	_, _, _, err := store.Open(store.Options{Dir: dir, FS: fsys, Now: fixedNow})
	if !errors.Is(err, storefault.ErrInjected) || !strings.Contains(err.Error(), "reading the size of the event log") {
		t.Fatalf("Open = %v, want the injected Stat failure", err)
	}
}

func TestOpenReportsALockFileItCannotOpen(t *testing.T) {
	committed, _ := committedLog(t)
	dir := seedLog(t, committed)
	if err := os.Mkdir(filepath.Join(dir, "events.jsonl.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := store.Open(store.Options{Dir: dir, Now: fixedNow})
	if err == nil || errors.Is(err, store.ErrLocked) || !strings.Contains(err.Error(), "opening the lock file") {
		t.Fatalf("Open = %v, want the lock-file error", err)
	}
}

func TestCheckReportsAMissingPath(t *testing.T) {
	_, err := store.Check(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Check of a missing path = %v, want fs.ErrNotExist", err)
	}
}

func TestOSCreateTempReportsAMissingDirectory(t *testing.T) {
	f, name, err := store.OS().CreateTemp(filepath.Join(t.TempDir(), "absent"), "probe-*")
	if !errors.Is(err, fs.ErrNotExist) || f != nil || name != "" {
		t.Fatalf("CreateTemp in a missing directory = %v, %q, %v; want no file and fs.ErrNotExist", f, name, err)
	}
}

func TestProbeReportsAFailedCloseOfTheLog(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpClose, Name: "events.jsonl"})
	if err := r.s.Probe(); !errors.Is(err, storefault.ErrInjected) || !strings.Contains(err.Error(), "closing the log") {
		t.Fatalf("Probe = %v, want the injected Close failure", err)
	}
}

// takenFS is a store.FS over the operating system on which every recovery
// sidecar name numbered below free already exists: creating one exclusively
// fails with fs.ErrExist, without a file being made. A free of zero leaves no
// name free.
type takenFS struct {
	*shortFS
	free  int
	tries int
}

func (f *takenFS) OpenFile(name string, flag int, perm os.FileMode) (store.File, error) {
	base := filepath.Base(name)
	if i := strings.LastIndex(base, "-"); strings.Contains(base, ".recovered-") && i >= 0 && flag&os.O_EXCL != 0 {
		f.tries++
		var n int
		if _, err := fmt.Sscan(base[i+1:], &n); err == nil && (f.free == 0 || n < f.free) {
			return nil, fmt.Errorf("creating %s: %w", name, fs.ErrExist)
		}
	}
	return f.shortFS.OpenFile(name, flag, perm)
}

// INV-LOG-10: the sidecar takes the first free name up to the 10000th, and a
// directory with all 10000 taken refuses the start and leaves the log.
func TestRecoverySidecarNameBound(t *testing.T) {
	t.Run("the 10000th name is the last one tried", func(t *testing.T) {
		_, original := tornLog(t)
		dir := seedLog(t, original)
		fsys := &takenFS{shortFS: noShortness(), free: 10000}
		_, _, rec := openStore(t, dir, fsys)
		if want := filepath.Join(dir, "events.jsonl.recovered-20261008T123045Z-10000"); rec.Sidecar != want {
			t.Errorf("Recovery.Sidecar = %q, want %q", rec.Sidecar, want)
		}
		if fsys.tries != 10000 {
			t.Errorf("%d names tried, want 10000", fsys.tries)
		}
	})
	t.Run("no name is free", func(t *testing.T) {
		_, original := tornLog(t)
		dir := seedLog(t, original)
		fsys := &takenFS{shortFS: noShortness()}
		_, _, _, err := store.Open(store.Options{Dir: dir, FS: fsys, Now: fixedNow})
		if err == nil || !strings.Contains(err.Error(), "no free recovery sidecar name") || !strings.Contains(err.Error(), "10000 tries") {
			t.Fatalf("Open = %v, want the refusal after 10000 tries", err)
		}
		if fsys.tries != 10000 {
			t.Errorf("%d names tried, want 10000", fsys.tries)
		}
		if got := readLog(t, dir); !bytes.Equal(got, original) {
			t.Error("a refused recovery changed the log")
		}
		if got := sidecars(t, dir); len(got) != 0 {
			t.Errorf("sidecars were made: %v", got)
		}
	})
}
