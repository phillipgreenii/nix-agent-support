// Package tokenfile owns the Jira token file the daemon hands to pjira: a
// 0600 file inside an owner-only subdirectory of the per-user temp directory,
// reached through a stable symlink inside the daemon state directory.
//
// The Store is a Repository with an atomic Replace: a reader never sees a
// partial or empty token file, and the stable link is never missing while it
// is repointed. The token value is never logged and never part of an error.
//
// The temp directory is passed in by the caller (the daemon passes
// os.TempDir(), read once at start: a documented exception to the daemon
// reading no environment variable), so tests use t.TempDir().
package tokenfile

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	// SubdirName is the owned subdirectory created inside the temp directory.
	SubdirName = "pg-connector-issue-jira"
	// FileName is the token file name inside the subdirectory.
	FileName = "jira-token"
)

// ErrUnsafeDir is the typed error for every fail-closed directory check. The
// caller maps it to the degraded reason token_file. Use errors.Is.
var ErrUnsafeDir = errors.New("tokenfile: unsafe token directory")

// DirError carries the path and the reason a directory was refused.
type DirError struct {
	Path   string
	Reason string
}

func (e *DirError) Error() string {
	return fmt.Sprintf("tokenfile: unsafe token directory %q: %s", e.Path, e.Reason)
}

// Is makes errors.Is(err, ErrUnsafeDir) match.
func (e *DirError) Is(target error) bool { return target == ErrUnsafeDir }

func unsafe(path, format string, args ...any) error {
	return &DirError{Path: path, Reason: fmt.Sprintf(format, args...)}
}

// Store manages the token file, its subdirectory and the stable link.
type Store struct {
	tempDir string
	subdir  string
	file    string
	link    string
	uid     uint32

	// rename is os.Rename; tests replace it to stand in for ENOSPC and EROFS.
	rename func(oldpath, newpath string) error
}

// New validates the temp directory and returns a Store. It fails closed with
// ErrUnsafeDir unless tempDir is absolute, not /tmp or /private/tmp, and an
// Lstat shows a directory owned by the daemon uid with no group or other
// permission bits. link must be an absolute path (the caller validates that
// it lies inside the state directory). New writes nothing.
func New(tempDir, link string) (*Store, error) {
	s := &Store{
		tempDir: tempDir,
		link:    link,
		uid:     uint32(os.Getuid()),
		rename:  os.Rename,
	}
	if tempDir == "" {
		return nil, unsafe(tempDir, "temp directory is unset")
	}
	if !filepath.IsAbs(tempDir) {
		return nil, unsafe(tempDir, "temp directory is not absolute")
	}
	clean := filepath.Clean(tempDir)
	if clean == "/tmp" || clean == "/private/tmp" {
		return nil, unsafe(tempDir, "temp directory is the shared /tmp")
	}
	if !filepath.IsAbs(link) {
		return nil, fmt.Errorf("tokenfile: token link %q is not absolute", link)
	}
	if err := s.checkDir(clean, 0); err != nil {
		return nil, err
	}
	s.tempDir = clean
	s.subdir = filepath.Join(clean, SubdirName)
	s.file = filepath.Join(s.subdir, FileName)
	return s, nil
}

// checkDir Lstats path: a real directory owned by the daemon uid. wantMode
// 0 means "no group or other bits"; otherwise the permission bits must equal
// wantMode exactly.
func (s *Store) checkDir(path string, wantMode os.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return unsafe(path, "lstat failed: %v", err)
	}
	if !fi.IsDir() { // Lstat: a symlink is not a directory here
		return unsafe(path, "not a real directory (mode %v)", fi.Mode())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return unsafe(path, "cannot read owner")
	}
	if st.Uid != s.uid {
		return unsafe(path, "owner uid %d is not the daemon uid %d", st.Uid, s.uid)
	}
	perm := fi.Mode().Perm()
	if wantMode == 0 {
		if perm&0o077 != 0 {
			return unsafe(path, "mode %#o grants group or other access", perm)
		}
	} else if perm != wantMode {
		return unsafe(path, "mode %#o is not %#o", perm, wantMode)
	}
	return nil
}

// Path returns the token file path inside the temp directory.
func (s *Store) Path() string { return s.file }

// Link returns the stable link path.
func (s *Store) Link() string { return s.link }

// ensureSubdir creates the subdirectory (0700, explicit Chmod) when absent
// and validates it. An existing directory must already be an owned 0700 real
// directory; it is never chmod-ed into compliance.
func (s *Store) ensureSubdir() error {
	if _, err := os.Lstat(s.subdir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(s.subdir, 0o700); err != nil {
			return fmt.Errorf("tokenfile: create subdirectory: %w", err)
		}
		if err := os.Chmod(s.subdir, 0o700); err != nil {
			return fmt.Errorf("tokenfile: chmod subdirectory: %w", err)
		}
	}
	return s.checkDir(s.subdir, 0o700)
}

// Start prepares the store. Call it after the instance lock is held and
// before the keychain read: it validates (or creates) the subdirectory and
// removes every leftover (the old link target, jira-token, jira-token.* and
// the old link). A leftover is never trusted or read.
func (s *Store) Start() error {
	if err := s.ensureSubdir(); err != nil {
		return err
	}
	return s.cleanup()
}

func (s *Store) cleanup() error {
	var errs []error
	// The old link target may live in a different temp directory (TMPDIR
	// changed between starts); remove it only when it is named like ours.
	if target, err := os.Readlink(s.link); err == nil && filepath.Base(target) == FileName {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(s.link); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	errs = append(errs, s.removeOwned()...)
	return errors.Join(errs...)
}

// removeOwned deletes the file and the jira-token.* temp files in the
// subdirectory.
func (s *Store) removeOwned() []error {
	var errs []error
	matches, err := filepath.Glob(filepath.Join(s.subdir, FileName+".*"))
	if err != nil {
		errs = append(errs, err)
	}
	for _, p := range append([]string{s.file}, matches...) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errs
}

// Write atomically replaces the token file with token (mode 0600) and then
// atomically repoints the stable link at it. A failure leaves the previous
// file untouched, removes the temp file and returns an error; it never
// panics. The error never contains the token.
func (s *Store) Write(token []byte) error {
	if err := s.ensureSubdir(); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.subdir, FileName+".*")
	if err != nil {
		return fmt.Errorf("tokenfile: create temp file: %w", err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("tokenfile: chmod temp file: %w", err)
	}
	if _, err := f.Write(token); err != nil {
		return fmt.Errorf("tokenfile: write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("tokenfile: sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("tokenfile: close temp file: %w", err)
	}
	if err := s.rename(tmp, s.file); err != nil {
		return fmt.Errorf("tokenfile: replace token file: %w", err)
	}
	committed = true
	syncDir(s.subdir)
	// Rewrites in steady state leave an already-correct link alone: on macOS
	// a concurrent open through a symlink that is being replaced can fail
	// with EINVAL, so the link is repointed only when it must change.
	if t, err := os.Readlink(s.link); err == nil && t == s.file {
		return nil
	}
	return s.repoint()
}

// syncDir fsyncs a directory, best effort.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// repoint creates a symlink under a temp name beside the link and renames it
// over the link, so the link is never missing.
func (s *Store) repoint() error {
	dir := filepath.Dir(s.link)
	base := filepath.Base(s.link)
	var tmp string
	for range 10 {
		cand := filepath.Join(dir, "."+base+"."+strconv.FormatUint(rand.Uint64(), 36))
		err := os.Symlink(s.file, cand)
		if err == nil {
			tmp = cand
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("tokenfile: create link: %w", err)
		}
	}
	if tmp == "" {
		return errors.New("tokenfile: create link: no free temp name")
	}
	if err := s.rename(tmp, s.link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tokenfile: replace link: %w", err)
	}
	return nil
}

// Touch sets the token file's atime and mtime to now, so the OS temp cleaner
// (about 3 days unused) leaves it alone. Call it every 6 hours. It needs no
// token.
func (s *Store) Touch() error {
	now := time.Now()
	if err := os.Chtimes(s.file, now, now); err != nil {
		return fmt.Errorf("tokenfile: touch: %w", err)
	}
	return nil
}

// Remove deletes the token file, the subdirectory entries the store owns and
// the link. Failures are joined and returned, never panicked on; callers log
// them and continue shutting down. The empty subdirectory itself is removed
// best effort.
func (s *Store) Remove() error {
	var errs []error
	if err := os.Remove(s.link); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	errs = append(errs, s.removeOwned()...)
	if err := os.Remove(s.subdir); err != nil && !errors.Is(err, os.ErrNotExist) {
		// A non-empty or foreign directory is left alone; not an error
		// worth reporting when the owned entries are gone.
		if len(errs) > 0 {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
