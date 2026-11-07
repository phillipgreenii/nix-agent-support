package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sourceConfigKey records, in each slot's own config, the golden checkout the
// slot was cloned from; Verify uses it to tell a slot from a foreign repo.
const sourceConfigKey = "pgcheckoutpool.source"

// CloneConfig configures a Clone provisioner. Repo is the golden checkout the
// slots are copy-on-write clones of: a clean, non-bare checkout with no
// linked worktrees and no running fsmonitor daemon, on the same APFS volume
// as every slot directory.
type CloneConfig = WorktreeConfig

// ErrCloneUnsupported: directory-level clonefile(2) is not available here
// (not macOS, or the volume does not support clones).
var ErrCloneUnsupported = errors.New("directory clone (clonefile) not supported")

// Clone is the CoW-clone Provisioner. Each slot is a complete, independent
// repository made by one clonefile(2) of the golden checkout (3 filesystem
// events regardless of tree size), so unlike Worktree slots share no .git
// administration with the golden checkout. Everything but creation and
// verification is the Worktree behaviour, run inside the slot's own repository.
type Clone struct {
	*Worktree
}

var _ Provisioner = (*Clone)(nil)

// NewClone validates cfg and returns the provisioner.
func NewClone(cfg CloneConfig) (*Clone, error) {
	w, err := NewWorktree(cfg)
	if err != nil {
		return nil, err
	}
	return &Clone{Worktree: w}, nil
}

// cloneDir clones src to dst with one clonefile(2) call. dst must not exist.
// The implementation is per-platform; see clone_darwin.go and clone_other.go.
var _ = cloneDir

// Create clones the golden checkout to dir, isolates the clone from the
// golden's worktree bookkeeping, applies the stat mitigations that keep the
// first git status cheap (the clone changes every inode and ctime), then
// cleans to the keep list and checks out commit detached with the
// post-checkout hook enabled: the one full hook run of the slot's life.
func (c *Clone) Create(ctx context.Context, dir, commit string) error {
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("clone destination %s already exists", dir)
	}
	if err := cloneDir(c.repo, dir); err != nil {
		return err
	}
	fail := func(err error) error {
		// A half-made slot must not look like a healthy one.
		_ = os.RemoveAll(dir)
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git", "worktrees")); err != nil {
		return fail(err)
	}
	for _, kv := range [][2]string{
		{"core.checkStat", "minimal"},
		{"core.trustctime", "false"},
		{sourceConfigKey, c.repo},
	} {
		if _, err := c.git(ctx, dir, "config", kv[0], kv[1]); err != nil {
			return fail(err)
		}
	}
	if _, err := c.git(ctx, dir, c.cleanArgs(false)...); err != nil {
		return fail(err)
	}
	if _, err := c.git(ctx, dir, "checkout", "-q", "-f", "--detach", commit); err != nil {
		return fail(err)
	}
	return nil
}

// Verify checks dir is a self-contained clone made from the configured golden
// checkout, with no stale lock.
func (c *Clone) Verify(ctx context.Context, dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s missing", ErrUnhealthy, dir)
	}
	top, err := c.git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, ErrWedged) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnhealthy, err)
	}
	if !samePath(top, dir) {
		return fmt.Errorf("%w: toplevel %s is not %s", ErrUnhealthy, top, dir)
	}
	common, err := c.git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if !samePath(common, filepath.Join(dir, ".git")) {
		return fmt.Errorf("%w: git directory %s is not the slot's own", ErrUnhealthy, common)
	}
	src, err := c.git(ctx, dir, "config", "--get", sourceConfigKey)
	if err != nil || !samePath(src, c.repo) {
		return fmt.Errorf("%w: not cloned from %s (recorded %q)", ErrUnhealthy, c.repo, src)
	}
	return c.lockPresent(ctx, dir)
}

// Reset is Worktree.Reset, after fetching from the golden checkout when the
// slot's own object store does not yet have commit (the slot's objects are a
// snapshot of the golden's at creation time).
func (c *Clone) Reset(ctx context.Context, dir, commit, branch string) (string, error) {
	if _, err := c.git(ctx, dir, "cat-file", "-e", commit+"^{commit}"); err != nil && !errors.Is(err, ErrWedged) {
		if _, ferr := c.git(ctx, dir, "fetch", "-q", "--no-tags", c.repo, "+refs/heads/*:refs/pool-source/*"); ferr != nil {
			if errors.Is(ferr, ErrWedged) || errors.Is(ferr, ErrStaleLock) {
				return "", ferr
			}
			return "", fmt.Errorf("fetch %s into slot: %w", strings.TrimSpace(c.repo), ferr)
		}
	}
	return c.Worktree.Reset(ctx, dir, commit, branch)
}
