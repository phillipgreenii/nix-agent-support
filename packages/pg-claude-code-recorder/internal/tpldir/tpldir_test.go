package tpldir

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func fixture(builds *atomic.Int32) *Template {
	return New(func(dir string) error {
		builds.Add(1)
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
			return err
		}
		for name, data := range map[string]string{"a.db": "alpha", "sub/b": "beta", "x.lock": "pid", "a.db-wal": ""} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
				return err
			}
		}
		return nil
	}, "x.lock")
}

func TestRestoreCopiesFilesAndDirsWithoutLockOrEmptyWAL(t *testing.T) {
	var builds atomic.Int32
	tp := fixture(&builds)
	dst := filepath.Join(t.TempDir(), "state")
	tp.Restore(t, dst)
	for name, want := range map[string]string{"a.db": "alpha", "sub/b": "beta"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || !bytes.Equal(got, []byte(want)) {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"x.lock", "a.db-wal"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			t.Fatalf("%s was restored", name)
		}
	}
}

func TestRestoreBuildsOnceAndEachCopyIsIndependent(t *testing.T) {
	var builds atomic.Int32
	tp := fixture(&builds)
	d1, d2 := t.TempDir(), t.TempDir()
	tp.Restore(t, d1)
	if err := os.WriteFile(filepath.Join(d1, "a.db"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	tp.Restore(t, d2)
	got, _ := os.ReadFile(filepath.Join(d2, "a.db"))
	if string(got) != "alpha" {
		t.Fatalf("second copy saw the first copy's write: %q", got)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("built %d times, want 1", n)
	}
}

func TestBuildRefusesALeftoverWAL(t *testing.T) {
	tp := New(func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "a.db-wal"), []byte("pages"), 0o600)
	})
	ft := &fakeT{TB: t}
	func() {
		defer func() { _ = recover() }()
		tp.Restore(ft, t.TempDir())
	}()
	if !ft.failed {
		t.Fatal("a non-empty WAL in the template was accepted")
	}
}

type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Fatalf(string, ...any) { f.failed = true; panic("fatal") }
func (f *fakeT) Helper()               {}
