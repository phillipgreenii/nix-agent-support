// Package sharedrepo lets the tests of one package share a single real git
// repository instead of building one per test.
//
// A fixture repository (git init, its configuration, the first commit) costs
// about half a second under load, which dominated the real-git tests of the
// worktree sweep and the executor. A test that only adds linked worktrees and
// branches can run against one repository for the whole package as long as the
// repository is returned to its post-fixture state before each test (Reset).
// A test that commits to the main checkout or changes repository configuration
// MUST build its own fixture with x/gittest instead.
//
// The repository comes from x/gitfixture (hermetic: temp root, fixture HOME,
// allowlisted git environment), so it never touches an ambient repository. The
// root is removed by Remove, which the package's TestMain calls after the run.
// Tests that share a repository MUST NOT run in parallel.
package sharedrepo

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/phillipgreenii/x/gitfixture"
)

// Shared builds one fixture repository on first use. The zero value is ready.
type Shared struct {
	once sync.Once
	root string
	repo *gitfixture.Repo
	err  error
}

// Get returns the repository, building it on the first call: an empty
// repository on branch main, identity derived from suite, then setup (which
// makes the commits every test starts from; nil means none). It returns the
// first call's error on every call.
func (s *Shared) Get(ctx context.Context, suite string, setup func(context.Context, *gitfixture.Repo) error) (*gitfixture.Repo, error) {
	s.once.Do(func() {
		root, err := os.MkdirTemp("", "sharedrepo-")
		if err != nil {
			s.err = err
			return
		}
		s.root = root
		repo, err := gitfixture.NewRepo(ctx, root, gitfixture.RepoOptions{Suite: suite, InitialBranch: "main"})
		if err == nil && setup != nil {
			err = setup(ctx, repo)
		}
		s.repo, s.err = repo, err
	})
	return s.repo, s.err
}

// Remove deletes the repository's root. Call it from TestMain after m.Run.
func (s *Shared) Remove() {
	if s.root != "" {
		_ = os.RemoveAll(s.root)
	}
}

// Reset returns repo to its post-fixture state: every linked worktree
// unregistered (a locked one is unlocked first, then pruned once its directory
// is gone) and no branch but main. Directories of linked worktrees are the
// test's own temp directories, which the test framework has removed by the time
// the next test starts.
func Reset(ctx context.Context, repo *gitfixture.Repo) error {
	c := repo.Client
	out, err := c.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("git worktree list: %w", err)
	}
	var path string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "locked") && path != repo.Dir:
			_, _ = c.Run(ctx, "worktree", "unlock", path)
		}
	}
	if _, err := c.Run(ctx, "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune: %w", err)
	}
	refs, err := c.Run(ctx, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return fmt.Errorf("git for-each-ref: %w", err)
	}
	for _, ref := range strings.Fields(string(refs)) {
		if ref == "refs/heads/main" {
			continue
		}
		if _, err := c.Run(ctx, "update-ref", "-d", ref); err != nil {
			return fmt.Errorf("git update-ref -d %s: %w", ref, err)
		}
	}
	return nil
}
