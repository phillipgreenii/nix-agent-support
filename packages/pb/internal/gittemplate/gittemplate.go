// Package gittemplate builds a seeded x/gitfixture repository ONCE per test
// process and hands each test its own byte copy.
//
// A fixture repository costs several git spawns (init, three config writes,
// then add, commit and rev-parse for every seeded commit), and under -race on a
// loaded host that setup was most of a git-backed test's CPU. The spawns are
// the same for every test, so the template is seeded once through the
// hermetic x/gitfixture library and each test receives a plain directory copy
// of the result, wrapped in a gitfixture.Repo whose Client carries the
// template's own hermetic option set (fixture HOME, no system config,
// discovery ceiling). The git is still real: only the repeated setup is shared.
//
// The package imports "testing" and is for _test.go files only.
package gittemplate

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
)

// Template is a lazily built, process-wide seeded repository. The zero value
// is not usable; construct it with New.
type Template struct {
	suite string
	seed  func(ctx context.Context, r *gitfixture.Repo) error

	once sync.Once
	root string
	repo *gitfixture.Repo
	err  error
}

// New returns a template for the fixture suite name suite. seed receives the
// freshly initialized repository and commits whatever the tests share; it runs
// once, on the first Instance call.
func New(suite string, seed func(ctx context.Context, r *gitfixture.Repo) error) *Template {
	return &Template{suite: suite, seed: seed}
}

func (tp *Template) build() {
	// A seed that cannot fail the test it runs under (it runs once for the
	// whole process) panics; report that as the template's error.
	defer func() {
		if r := recover(); r != nil {
			tp.err = fmt.Errorf("seeding template %s panicked: %v", tp.suite, r)
		}
	}()
	root, err := os.MkdirTemp("", "pb-gittemplate-"+tp.suite+"-")
	if err != nil {
		tp.err = err
		return
	}
	tp.root = root
	ctx := context.Background()
	repo, err := gitfixture.NewRepo(ctx, filepath.Join(root, "tpl"), gitfixture.RepoOptions{Suite: tp.suite})
	if err != nil {
		tp.err = err
		return
	}
	if err := tp.seed(ctx, repo); err != nil {
		tp.err = fmt.Errorf("seeding template %s: %w", tp.suite, err)
		return
	}
	tp.repo = repo
}

// Close removes the template's directory. Call it from TestMain after m.Run.
func (tp *Template) Close() {
	if tp.root != "" {
		_ = os.RemoveAll(tp.root)
	}
}

// Instance returns a private copy of the template repository under a fresh
// t.TempDir(), ready for the test to mutate. The copy is a complete repository
// (objects, refs, config) and shares no state with the template or with other
// instances.
func (tp *Template) Instance(t *testing.T) *gitfixture.Repo {
	t.Helper()
	tp.once.Do(tp.build)
	if tp.err != nil {
		t.Fatalf("gittemplate: %v", tp.err)
	}
	dest := filepath.Join(t.TempDir(), "repo")
	if err := copyTree(tp.repo.Dir, dest); err != nil {
		t.Fatalf("gittemplate: copying template: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatalf("gittemplate: resolving %s: %v", dest, err)
	}
	c, err := gitclient.New(t.Context(), resolved, tp.repo.Options()...)
	if err != nil {
		t.Fatalf("gittemplate: opening client at %s: %v", resolved, err)
	}
	return &gitfixture.Repo{Dir: resolved, Client: c}
}

// copyTree copies the directory tree src to dst, preserving file modes and
// symlinks. It spawns no process.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
