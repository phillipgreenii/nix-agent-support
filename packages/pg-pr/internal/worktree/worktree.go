// Package worktree manages git worktrees for PR review.
//
// It ports the behavior of the Python `gh-prreview` package's
// checkout/list-local/remove commands to Go. The package exposes three
// top-level operations:
//
//   - Add — fetch a PR and create a git worktree at <root>/pr-<number>.
//   - Remove — remove a PR's worktree and (best-effort) its local branch.
//   - List — enumerate PR worktrees under <root>.
//
// Worktree paths follow the convention <root>/pr-<number>. The branch
// inside each worktree is named review/pr-<number>, fetched from the
// remote PR ref pull/<number>/head.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Worktree describes a single PR review worktree on disk.
type Worktree struct {
	PRNumber             int    `json:"pr_number"`
	Path                 string `json:"path"`
	Branch               string `json:"branch"`
	HasUncommittedChange bool   `json:"has_uncommitted_changes"`
	UnpushedCommits      int    `json:"unpushed_commits"`
}

// Options carries inputs shared by all worktree operations.
type Options struct {
	// WorktreeRoot is the directory under which per-PR worktrees live.
	// Required.
	WorktreeRoot string

	// RepoDir is the git repository whose remote will be used for `git fetch`
	// and from which `git worktree add` is issued. If empty, the current
	// working directory is used.
	RepoDir string

	// Force, when true, allows `git worktree remove --force` and
	// `git branch -D` even with uncommitted changes / unmerged branches.
	Force bool

	// NoFetch, when true, skips the `git fetch` of the PR head — used when the
	// caller (e.g. the pg-pr daemon) has already fetched the head, so the
	// worktree is built from the already-local origin/pr/<pr> ref and no SSH
	// credential / `step` is needed here. Add also skips the fetch
	// automatically when that ref is already present.
	NoFetch bool

	// Git, GH are injected for testing. If nil, real CLI-backed
	// implementations are used.
	Git GitClient
	GH  GHClient
}

// resolve fills in default Git/GH clients and validates required fields.
func (o *Options) resolve() error {
	if o.WorktreeRoot == "" {
		return errors.New("worktree root is required")
	}
	if o.Git == nil {
		o.Git = NewCLIGitClient()
	}
	if o.GH == nil {
		o.GH = NewCLIGHClient()
	}
	return nil
}

// AddResult describes the outcome of an Add call.
type AddResult struct {
	PRNumber      int    `json:"pr_number"`
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	AlreadyExists bool   `json:"already_exists"`
}

// RemoveResult describes the outcome of a Remove call for a single PR.
type RemoveResult struct {
	PRNumber   int    `json:"pr_number"`
	Path       string `json:"path"`
	Removed    bool   `json:"removed"`
	Skipped    bool   `json:"skipped"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// Add fetches PR <pr> and creates a worktree at <root>/pr-<pr>.
//
// If the worktree path already exists, Add is a no-op and returns
// AlreadyExists=true. Otherwise it fetches refs/pull/<pr>/head from the
// configured remote and creates a new branch review/pr-<pr> in the
// worktree.
func Add(ctx context.Context, pr int, opts Options) (*AddResult, error) {
	if err := opts.resolve(); err != nil {
		return nil, err
	}
	if pr <= 0 {
		return nil, fmt.Errorf("invalid PR number: %d", pr)
	}

	repoDir := opts.RepoDir
	if repoDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		repoDir = cwd
	}

	owner, repo, err := opts.Git.RepoFromRemote(ctx, repoDir)
	if err != nil {
		return nil, fmt.Errorf("detect repo from remote: %w", err)
	}

	// Best-effort: ensure PR actually exists. We rely on `gh pr view`'s
	// existence check rather than a separate API path.
	if _, err := opts.GH.PRExists(ctx, owner, repo, pr); err != nil {
		return nil, fmt.Errorf("fetch PR #%d metadata: %w", pr, err)
	}

	target := filepath.Join(opts.WorktreeRoot, fmt.Sprintf("pr-%d", pr))
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		return &AddResult{
			PRNumber:      pr,
			Path:          target,
			Branch:        fmt.Sprintf("review/pr-%d", pr),
			AlreadyExists: true,
		}, nil
	}

	if err := os.MkdirAll(opts.WorktreeRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree root: %w", err)
	}

	branch := fmt.Sprintf("review/pr-%d", pr)
	startPoint := fmt.Sprintf("origin/pr/%d", pr)

	// Skip the fetch when the caller asked (NoFetch) or when the PR head ref
	// is already local (the pg-pr daemon pre-fetches it). This keeps the
	// SSH-cert `step` machinery out of the reviewer / subagent environment.
	skipFetch := opts.NoFetch
	if !skipFetch {
		if present, _ := opts.Git.RefExists(ctx, repoDir, startPoint); present {
			skipFetch = true
		}
	}
	if !skipFetch {
		if err := opts.Git.FetchPR(ctx, repoDir, pr); err != nil {
			return nil, fmt.Errorf("git fetch pull/%d/head: %w", pr, err)
		}
	}

	if err := opts.Git.CreateWorktree(ctx, repoDir, target, branch, startPoint); err != nil {
		return nil, fmt.Errorf("git worktree add: %w", err)
	}

	return &AddResult{
		PRNumber: pr,
		Path:     target,
		Branch:   branch,
	}, nil
}

// Remove removes PR <pr>'s worktree (and best-effort deletes its branch).
//
// If the worktree path does not exist, Remove returns a result with
// Skipped=true and a clear reason. Uncommitted changes cause Skipped=true
// unless opts.Force is set.
func Remove(ctx context.Context, pr int, opts Options) (*RemoveResult, error) {
	if err := opts.resolve(); err != nil {
		return nil, err
	}
	if pr <= 0 {
		return nil, fmt.Errorf("invalid PR number: %d", pr)
	}

	target := filepath.Join(opts.WorktreeRoot, fmt.Sprintf("pr-%d", pr))
	res := &RemoveResult{PRNumber: pr, Path: target}

	info, statErr := os.Stat(target)
	if os.IsNotExist(statErr) {
		res.Skipped = true
		res.SkipReason = fmt.Sprintf("no worktree found for PR %d at %s", pr, target)
		return res, nil
	}
	if statErr != nil {
		return nil, fmt.Errorf("stat worktree: %w", statErr)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("worktree path is not a directory: %s", target)
	}

	wt, err := opts.Git.WorktreeInfo(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("inspect worktree: %w", err)
	}

	if !opts.Force && wt.HasUncommittedChange {
		res.Skipped = true
		res.SkipReason = "worktree has uncommitted changes; rerun with --force to remove"
		return res, nil
	}

	repoDir := opts.RepoDir
	if repoDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		repoDir = cwd
	}

	if err := opts.Git.RemoveWorktree(ctx, repoDir, target, opts.Force); err != nil {
		return nil, fmt.Errorf("git worktree remove: %w", err)
	}

	// Best-effort: delete branch. Failure is non-fatal (branch may be
	// checked out elsewhere or already gone).
	_ = opts.Git.DeleteBranch(ctx, repoDir, wt.Branch, opts.Force)

	// Best-effort: prune stale admin entries.
	_ = opts.Git.PruneWorktrees(ctx, repoDir)

	res.Removed = true
	return res, nil
}

// List enumerates PR worktrees under opts.WorktreeRoot.
//
// Only directories matching pr-<digits> are returned. Each entry is
// inspected via `git -C` to populate branch, uncommitted, and unpushed
// fields. Results are sorted by PR number ascending.
func List(ctx context.Context, opts Options) ([]Worktree, error) {
	if err := opts.resolve(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(opts.WorktreeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []Worktree{}, nil
		}
		return nil, fmt.Errorf("read worktree root: %w", err)
	}

	var out []Worktree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "pr-") {
			continue
		}
		numStr := strings.TrimPrefix(name, "pr-")
		pr, convErr := strconv.Atoi(numStr)
		if convErr != nil || pr <= 0 {
			continue
		}

		path := filepath.Join(opts.WorktreeRoot, name)
		wt, infoErr := opts.Git.WorktreeInfo(ctx, path)
		if infoErr != nil {
			// Skip non-worktree directories silently — matches Python.
			continue
		}
		// PRNumber from path is authoritative.
		wt.PRNumber = pr
		out = append(out, *wt)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].PRNumber < out[j].PRNumber })
	return out, nil
}
