package tokenfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// privateDir returns a t.TempDir tightened to 0700, like a per-user TMPDIR.
func privateDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func newStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	tmp := privateDir(t)
	state := privateDir(t)
	link := filepath.Join(state, "jira-token")
	s, err := New(tmp, link)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s, tmp, state
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestModesAndOwner(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("tok")); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, s.Path()); m != 0o600 {
		t.Errorf("file mode %#o", m)
	}
	if m := mode(t, s.subdir); m != 0o700 {
		t.Errorf("subdir mode %#o", m)
	}
	fi, _ := os.Lstat(s.Path())
	if fi.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Error("file owner is not the daemon uid")
	}
}

func TestNewRefusesUnsafeTempDir(t *testing.T) {
	state := t.TempDir()
	link := filepath.Join(state, "l")
	groupReadable := t.TempDir()
	if err := os.Chmod(groupReadable, 0o750); err != nil {
		t.Fatal(err)
	}
	real := privateDir(t)
	symlinked := filepath.Join(privateDir(t), "sym")
	if err := os.Symlink(real, symlinked); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"unset":          "",
		"relative":       "var/tmp",
		"tmp":            "/tmp",
		"tmp slash":      "/tmp/",
		"private tmp":    "/private/tmp",
		"group readable": groupReadable,
		"symlink":        symlinked,
		"missing":        filepath.Join(real, "nope"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(dir, link); !errors.Is(err, ErrUnsafeDir) {
				t.Fatalf("want ErrUnsafeDir, got %v", err)
			}
		})
	}
	if _, err := os.Lstat(link); err == nil {
		t.Error("a link was written")
	}
}

func TestWrongOwnerRefused(t *testing.T) {
	s, _, _ := newStore(t)
	s.uid++ // stand-in for a directory owned by someone else
	if err := s.ensureSubdir(); !errors.Is(err, ErrUnsafeDir) {
		t.Fatalf("got %v", err)
	}
}

func TestExistingSubdirChecks(t *testing.T) {
	for name, prep := range map[string]func(t *testing.T, tmp string){
		"wrong mode": func(t *testing.T, tmp string) {
			p := filepath.Join(tmp, SubdirName)
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, tmp string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(tmp, SubdirName)); err != nil {
				t.Fatal(err)
			}
		},
		"regular file": func(t *testing.T, tmp string) {
			if err := os.WriteFile(filepath.Join(tmp, SubdirName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := privateDir(t)
			prep(t, tmp)
			s, err := New(tmp, filepath.Join(t.TempDir(), "l"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Start(); !errors.Is(err, ErrUnsafeDir) {
				t.Fatalf("Start: %v", err)
			}
			if err := s.Write([]byte("tok")); !errors.Is(err, ErrUnsafeDir) {
				t.Fatalf("Write: %v", err)
			}
			if _, err := os.Lstat(s.Link()); err == nil {
				t.Error("link written despite refusal")
			}
		})
	}
}

// readLoop reads path until stop closes, returning the first value for which
// valid is false (or "").
func readLoop(path string, stop <-chan struct{}, valid func([]byte) bool) string {
	return readLoopTol(path, stop, valid, false)
}

// readLoopTol is readLoop; tolerateEINVAL accepts the transient EINVAL macOS
// can return for an open through a symlink while it is being replaced (the
// link is still never missing).
func readLoopTol(path string, stop <-chan struct{}, valid func([]byte) bool, tolerateEINVAL bool) string {
	for {
		select {
		case <-stop:
			return ""
		default:
		}
		b, err := os.ReadFile(path)
		if err != nil {
			if tolerateEINVAL && errors.Is(err, syscall.EINVAL) {
				continue
			}
			return "read error: " + err.Error()
		}
		if !valid(b) {
			return "unexpected content"
		}
	}
}

func TestAtomicReplaceNoPartialReads(t *testing.T) {
	s, _, _ := newStore(t)
	toks := [][]byte{bytes.Repeat([]byte("a"), 100000), bytes.Repeat([]byte("b"), 10), bytes.Repeat([]byte("c"), 50000)}
	if err := s.Write(toks[0]); err != nil {
		t.Fatal(err)
	}
	valid := func(b []byte) bool {
		for _, tk := range toks {
			if bytes.Equal(b, tk) {
				return true
			}
		}
		return false
	}
	stop := make(chan struct{})
	res := make(chan string, 2)
	for _, p := range []string{s.Path(), s.Link()} { // direct and through the link
		go func() { res <- readLoop(p, stop, valid) }()
	}
	for i := 0; i < 100; i++ {
		if err := s.Write(toks[i%3]); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	for range 2 {
		if r := <-res; r != "" {
			t.Fatalf("reader saw: %s", r)
		}
	}
}

func TestNonAtomicVariantIsCaught(t *testing.T) {
	// Negative control: the same validity check must flag a truncate-then-write
	// variant. The writer pauses between truncate and write, deterministically.
	path := filepath.Join(t.TempDir(), "tok")
	full := []byte("complete-token")
	if err := os.WriteFile(path, full, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := os.ReadFile(path) // the reader runs in the truncation window
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(b, full) {
		t.Fatal("negative control did not observe the non-atomic window")
	}
}

func TestWideFileCorrected(t *testing.T) {
	s, _, _ := newStore(t)
	if err := os.WriteFile(s.Path(), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Path(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, s.Path()); m != 0o600 {
		t.Errorf("mode %#o, want 0600", m)
	}
}

func TestLinkNeverMissingWhileRepointed(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	res := make(chan string, 1)
	go func() {
		res <- readLoopTol(s.Link(), stop, func(b []byte) bool { return len(b) > 0 }, true)
	}()
	for range 100 {
		if err := s.repoint(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	if r := <-res; r != "" {
		t.Fatalf("link reader saw: %s", r)
	}
	if got, _ := os.ReadFile(s.Link()); string(got) != "one" {
		t.Errorf("through link: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Link()))
	if len(entries) != 1 {
		t.Errorf("temp links left behind: %v", entries)
	}
}

func TestLinkRepointedWhenTempDirChanges(t *testing.T) {
	state := privateDir(t)
	link := filepath.Join(state, "jira-token")
	var stores []*Store
	for i, tok := range []string{"first", "second"} {
		tmp := privateDir(t)
		s, err := New(tmp, link)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		if err := s.Write([]byte(tok)); err != nil {
			t.Fatal(err)
		}
		stores = append(stores, s)
		if got, _ := os.ReadFile(link); string(got) != tok {
			t.Fatalf("start %d: link reads %q", i, got)
		}
	}
	// The second Start removed the first start's file in the other temp dir.
	if _, err := os.Lstat(stores[0].Path()); err == nil {
		t.Error("old target survived the second start")
	}
}

func TestDanglingLinkFailsCleanly(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("tok")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	_, err := os.ReadFile(s.Link())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want a clean not-exist error, got %v", err)
	}
}

func TestStartCleansLeftovers(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("stale")); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(s.subdir, "jira-token.123456")
	if err := os.WriteFile(left, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A "restarted" store sharing the same dirs.
	s2, err := New(s.tempDir, s.Link())
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Start(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path(), left} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s survived Start", p)
		}
	}
	if _, err := os.Lstat(s.Link()); err == nil {
		t.Error("old link survived Start")
	}
}

func TestTouchMovesTimes(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("tok")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(s.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(s.Path())
	if !fi.ModTime().After(old.Add(71 * time.Hour)) {
		t.Errorf("mtime %v not moved", fi.ModTime())
	}
	if at := atime(fi); !at.After(old.Add(71 * time.Hour)) {
		t.Errorf("atime %v not moved", at)
	}
	if got, _ := os.ReadFile(s.Path()); string(got) != "tok" {
		t.Error("Touch altered content")
	}
}

func TestTouchMissingFileErrors(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Touch(); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestRemove(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("tok")); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(s.subdir, "jira-token.99")
	if err := os.WriteFile(extra, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path(), extra, s.Link(), s.subdir} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s survived Remove", p)
		}
	}
	if err := s.Remove(); err != nil {
		t.Errorf("second Remove: %v", err)
	}
}

func TestRemoveReportsFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s, _, state := newStore(t)
	if err := s.Write([]byte("tok")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o500); err != nil { // link cannot be unlinked
		t.Fatal(err)
	}
	defer os.Chmod(state, 0o700) //nolint:errcheck
	if err := s.Remove(); err == nil {
		t.Fatal("want a reported failure")
	}
}

func TestWriteFailureKeepsPreviousFile(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Write([]byte("previous")); err != nil {
		t.Fatal(err)
	}
	s.rename = func(string, string) error { return syscall.ENOSPC }
	err := s.Write([]byte("new-token-value"))
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "new-token-value") {
		t.Error("error leaks the token")
	}
	if got, _ := os.ReadFile(s.Path()); string(got) != "previous" {
		t.Errorf("previous file changed: %q", got)
	}
	entries, _ := os.ReadDir(s.subdir)
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestReadOnlySubdirKeepsPreviousFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s, _, _ := newStore(t)
	if err := s.Write([]byte("previous")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.subdir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(s.subdir, 0o700) //nolint:errcheck
	if err := s.Write([]byte("new")); err == nil {
		t.Fatal("want error")
	}
	if got, _ := os.ReadFile(s.Path()); string(got) != "previous" {
		t.Errorf("previous file changed: %q", got)
	}
}

func TestReadOnlyStateDirLinkFailureNoPanic(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s, _, state := newStore(t)
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(state, 0o700) //nolint:errcheck
	if err := s.Write([]byte("two")); err == nil {
		t.Fatal("want link error")
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Errorf("state dir polluted: %v", entries)
	}
}

func TestConcurrentWritesSafe(t *testing.T) {
	s, _, _ := newStore(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				_ = s.Write([]byte("tok"))
			}
		}()
	}
	wg.Wait()
	if got, _ := os.ReadFile(s.Link()); string(got) != "tok" {
		t.Errorf("got %q", got)
	}
}
