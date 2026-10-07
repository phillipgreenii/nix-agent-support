// Package lock provides a per-session advisory file lock: a single writer per
// conversation, so a resume can't race a send and two writers can't corrupt one
// transcript.
//
// # Lock-file lifecycle (bead pg2-bjhoq)
//
// A lock is a flock(2) on the file <dir>/<name>.lock. The file is created on
// first use and, since the garbage collector (RemoveIfFree) exists, may be
// UNLINKED later. Unlinking a flock file is only safe if every opener tolerates
// it, so the contract is:
//
//   - Lock opens the path, takes the flock, and then RE-VERIFIES that the path
//     still names the very inode it locked (fstat vs stat, os.SameFile). If the
//     file was unlinked (or unlinked and recreated) between open and flock, the
//     lock it holds is on a dead inode, so it drops it and retries from the
//     open. This closes the classic open-then-unlink race: two openers can
//     never end up each holding "the" lock on two different inodes.
//   - RemoveIfFree unlinks only while HOLDING the exclusive flock (taken
//     non-blocking, on a file opened WITHOUT O_CREATE). An opener that is
//     already holding the flock makes the removal fail with "held"; an opener
//     that has the file open but is still waiting for the flock acquires it
//     only after the remover releases, then fails the inode re-verification
//     and retries on a fresh file. Once an opener has verified, the remover can
//     no longer obtain the flock, so the verified inode cannot be unlinked
//     under it.
//
// A pre-GC ccpool binary has no re-verification; its only exposure is a CLI
// invocation that straddles the exact moment of an unlink of a lock whose
// session has no row and is more than 24h old, which cannot be a live session.
package lock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

// suffix is the lock-file extension; only names ending in it are ever listed or
// removed.
const suffix = ".lock"

type Flock struct{ dir string }

// New returns a Locker whose lockfiles live under dir (the runtime dir).
func New(dir string) *Flock { return &Flock{dir: dir} }

func (f *Flock) path(name string) string { return filepath.Join(f.dir, name+suffix) }

// Lock blocks until the per-name lock is held; the returned func releases it.
// The lock is held on the inode the path names at the moment of acquisition
// (see the package comment): if the file is unlinked or replaced while Lock
// waits, it retries on the new file.
func (f *Flock) Lock(name string) (func(), error) {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	for {
		fl := flock.New(f.path(name))
		if err := fl.Lock(); err != nil {
			return nil, fmt.Errorf("flock %q: %w", name, err)
		}
		held, herr := fl.Stat() // fstat of the locked descriptor
		onDisk, perr := os.Stat(f.path(name))
		if herr == nil && perr == nil && os.SameFile(held, onDisk) {
			return func() { _ = fl.Unlock() }, nil
		}
		// The path no longer names the inode we locked (a GC unlinked it while
		// we waited). Drop this stale lock and take it again on the live file.
		_ = fl.Unlock()
	}
}

// Entry is one lock file found by List.
type Entry struct {
	// Name is the lock name: the file name without its ".lock" suffix.
	Name    string
	ModTime time.Time
}

// List returns the regular files in the lock directory whose names end in
// ".lock". A missing directory is an empty list.
func (f *Flock) List() ([]Entry, error) {
	des, err := os.ReadDir(f.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		n := de.Name()
		if !strings.HasSuffix(n, suffix) || len(n) == len(suffix) {
			continue
		}
		fi, err := de.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue // vanished, or a directory/symlink: never ours to remove
		}
		out = append(out, Entry{Name: strings.TrimSuffix(n, suffix), ModTime: fi.ModTime()})
	}
	return out, nil
}

// RemoveIfFree unlinks the lock file for name when, and only when, nobody holds
// it AND confirm approves. It reports whether the file was removed.
//
// The sequence, all while holding the exclusive flock on the file's inode:
// open WITHOUT O_CREATE (a vanished file is "not removed", never recreated);
// non-blocking exclusive flock (held by anyone, including this process, means
// "not removed"); verify the path still names the locked inode; call confirm
// with the file's current mtime (a caller's last-moment re-check, race-free
// against any creator that takes the same lock before acting); unlink; release.
// See the package comment for why openers cannot race this.
func (f *Flock) RemoveIfFree(name string, confirm func(modTime time.Time) (bool, error)) (bool, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return false, fmt.Errorf("lock: refusing to remove unsafe name %q", name)
	}
	p := f.path(name)
	fh, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = fh.Close() }() // closing drops the flock, AFTER the unlink below
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil // held: in use
		}
		return false, err
	}
	held, err := fh.Stat()
	if err != nil {
		return false, err
	}
	if !held.Mode().IsRegular() {
		return false, nil
	}
	onDisk, err := os.Lstat(p)
	if err != nil || !os.SameFile(held, onDisk) {
		return false, nil // already replaced or removed by someone else
	}
	ok, err := confirm(held.ModTime())
	if err != nil || !ok {
		return false, err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
