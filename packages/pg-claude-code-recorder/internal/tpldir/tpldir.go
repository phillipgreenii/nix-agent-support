// Package tpldir builds a directory once per test process and restores a copy
// of it into each test's own directory. A test that needs a ready-made state
// directory (a store whose schema already exists) pays the build once, not once
// per test.
//
// The built directory is read into memory and its scratch location removed
// at once, so nothing outlives the build and no TestMain is needed. Each Restore
// writes plain files, so a test never shares a file, a WAL or a lock with
// another test, and tests stay independent and order-free.
package tpldir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// skipName reports a name that is never part of a template: a lock file is
// process state recreated by its owner, and a WAL or shm side file belongs to a
// live connection (Build refuses a template that leaves a non-empty one).
func skipName(name string, lockNames []string) bool {
	for _, l := range lockNames {
		if name == l {
			return true
		}
	}
	return false
}

type entry struct {
	rel  string
	dir  bool
	mode fs.FileMode
	data []byte
}

// Template is a directory built once by build and restored on demand.
type Template struct {
	build     func(dir string) error
	lockNames []string
	once      sync.Once
	entries   []entry
	err       error
}

// New returns a Template. build fills an empty directory and MUST close every
// handle it opened there (so SQLite has checkpointed and removed its WAL).
// lockNames are file names to leave out of every restore (for example a store's
// lock file).
func New(build func(dir string) error, lockNames ...string) *Template {
	return &Template{build: build, lockNames: lockNames}
}

func (tp *Template) load() {
	tmp, err := os.MkdirTemp("", "tpldir-")
	if err != nil {
		tp.err = err
		return
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := tp.build(tmp); err != nil {
		tp.err = err
		return
	}
	tp.err = filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tmp, p)
		if err != nil || rel == "." {
			return err
		}
		if skipName(d.Name(), tp.lockNames) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			tp.entries = append(tp.entries, entry{rel: rel, dir: true, mode: info.Mode().Perm()})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tpldir: %s is not a regular file", rel)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if (strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm")) && len(b) > 0 {
			return fmt.Errorf("tpldir: %s left behind by the build; close every handle first", rel)
		}
		if strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") {
			return nil
		}
		tp.entries = append(tp.entries, entry{rel: rel, mode: info.Mode().Perm(), data: b})
		return nil
	})
}

// Restore writes a copy of the template into dst, which it creates when it is
// missing. It fails the test on any error.
func (tp *Template) Restore(t testing.TB, dst string) {
	t.Helper()
	tp.once.Do(tp.load)
	if tp.err != nil {
		t.Fatalf("tpldir: build template: %v", tp.err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, e := range tp.entries {
		p := filepath.Join(dst, e.rel)
		if e.dir {
			if err := os.MkdirAll(p, e.mode); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, e.data, e.mode); err != nil {
			t.Fatal(err)
		}
	}
}
