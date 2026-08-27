package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitenv"
)

// GitClient abstracts the git operations the worktree package needs.
// The default implementation shells out to the `git` CLI; tests inject
// fakes.
type GitClient interface {
	// RepoFromRemote parses `git -C dir config --get remote.origin.url`
	// and returns the GitHub owner/repo it points to.
	RepoFromRemote(ctx context.Context, dir string) (owner, repo string, err error)

	// FetchPR fetches refs/pull/<pr>/head into refs/remotes/origin/pr/<pr>.
	FetchPR(ctx context.Context, dir string, pr int) error

	// RefExists reports whether ref resolves to a commit in dir (e.g.
	// "origin/pr/12"). Used to skip a redundant fetch when the PR head has
	// already been fetched (by the daemon's pre-fetch gate or an earlier run).
	// Returns (false, nil) when the ref is absent or on any error — a false
	// negative is safe (the caller just fetches anyway).
	//
	// Note: this only checks ref *presence*, not *currency* — it cannot tell a
	// freshly-fetched head from a stale one. That's safe in production because
	// the daemon's pre-fetch gate force-updates the ref before Add's caller ever
	// runs. In a gate-disabled fallback, a stale local origin/pr/<pr> would be
	// reviewed as-is (see Add's auto-skip below).
	RefExists(ctx context.Context, dir, ref string) (bool, error)

	// CreateWorktree runs `git worktree add <target> -b <branch> <startPoint>`
	// from inside dir.
	CreateWorktree(ctx context.Context, dir, target, branch, startPoint string) error

	// RemoveWorktree runs `git worktree remove [--force] <target>`.
	RemoveWorktree(ctx context.Context, dir, target string, force bool) error

	// PruneWorktrees runs `git worktree prune`.
	PruneWorktrees(ctx context.Context, dir string) error

	// DeleteBranch runs `git branch -d|-D <branch>`.
	DeleteBranch(ctx context.Context, dir, branch string, force bool) error

	// BranchAheadOfRef reports whether branch has any commits not reachable
	// from ref, via `git rev-list --count ref..branch`. Used before a force
	// delete to tell "unmerged into the primary branch but still identical
	// to (or behind) the PR's fetched head, so nothing is lost" apart from
	// "has genuine local-only commits beyond that head, so force-deleting
	// would destroy them".
	BranchAheadOfRef(ctx context.Context, dir, branch, ref string) (bool, error)

	// WorktreeInfo returns metadata about the worktree rooted at path.
	// It returns an error if path is not a git worktree.
	WorktreeInfo(ctx context.Context, path string) (*Worktree, error)
}

// CLIGitClient invokes the system `git` binary.
type CLIGitClient struct{}

// NewCLIGitClient returns a GitClient backed by the system `git` binary.
func NewCLIGitClient() GitClient { return &CLIGitClient{} }

var ghRemoteRE = regexp.MustCompile(`github\.com[:/]([^/]+)/(.+?)(?:\.git)?$`)

func (g *CLIGitClient) RepoFromRemote(ctx context.Context, dir string) (string, string, error) {
	out, err := runGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", "", err
	}
	url := strings.TrimSpace(out)
	m := ghRemoteRE.FindStringSubmatch(url)
	if m == nil {
		return "", "", fmt.Errorf("remote.origin.url is not a github URL: %q", url)
	}
	return m[1], m[2], nil
}

func (g *CLIGitClient) FetchPR(ctx context.Context, dir string, pr int) error {
	// Force-update (+) and disable prune: dir may already have
	// refs/remotes/origin/pr/<pr> from a prior fetch (this call re-fetches on
	// every re-review), and without both of these git's default prune pass
	// deletes that tracking ref (it doesn't match the default
	// refs/heads/*:refs/remotes/origin/* fetch refspec's source side) and then
	// fails to recreate it — see CLIPRFetcher.FetchPRHead in
	// internal/sync/prefetch.go, whose refspec this mirrors exactly.
	refspec := fmt.Sprintf("+pull/%d/head:refs/remotes/origin/pr/%d", pr, pr)
	_, err := runGit(ctx, dir, "fetch", "--no-prune", "origin", refspec)
	return err
}

func (g *CLIGitClient) RefExists(ctx context.Context, dir, ref string) (bool, error) {
	// `rev-parse --verify --quiet <ref>^{commit}` exits 0 when the ref resolves
	// to a commit and non-zero (empty output) otherwise. runGit returns an
	// error on any non-zero exit; treat every error as "not present".
	if _, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return false, nil
	}
	return true, nil
}

func (g *CLIGitClient) CreateWorktree(ctx context.Context, dir, target, branch, startPoint string) error {
	args := []string{"worktree", "add", target, "-b", branch}
	if startPoint != "" {
		args = append(args, startPoint)
	}
	_, err := runGit(ctx, dir, args...)
	return err
}

func (g *CLIGitClient) RemoveWorktree(ctx context.Context, dir, target string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, target)
	_, err := runGit(ctx, dir, args...)
	return err
}

func (g *CLIGitClient) PruneWorktrees(ctx context.Context, dir string) error {
	_, err := runGit(ctx, dir, "worktree", "prune")
	return err
}

func (g *CLIGitClient) DeleteBranch(ctx context.Context, dir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := runGit(ctx, dir, "branch", flag, branch)
	return err
}

func (g *CLIGitClient) BranchAheadOfRef(ctx context.Context, dir, branch, ref string) (bool, error) {
	out, err := runGit(ctx, dir, "rev-list", "--count", ref+".."+branch)
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, fmt.Errorf("parse rev-list --count output %q: %w", out, err)
	}
	return n > 0, nil
}

func (g *CLIGitClient) WorktreeInfo(ctx context.Context, path string) (*Worktree, error) {
	// Branch name.
	branchOut, err := runGit(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	branch := strings.TrimSpace(branchOut)

	// Uncommitted changes.
	statusOut, err := runGit(ctx, path, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	hasChanges := strings.TrimSpace(statusOut) != ""

	// Unpushed commits (0 if no upstream).
	unpushed := 0
	if _, err := runGit(ctx, path, "rev-parse", "@{u}"); err == nil {
		if cnt, err := runGit(ctx, path, "rev-list", "--count", "@{u}..HEAD"); err == nil {
			if n, parseErr := strconv.Atoi(strings.TrimSpace(cnt)); parseErr == nil {
				unpushed = n
			}
		}
	}

	return &Worktree{
		// PRNumber filled in by caller (or pulled from path elsewhere).
		Path:                 path,
		Branch:               branch,
		HasUncommittedChange: hasChanges,
		UnpushedCommits:      unpushed,
	}, nil
}

// runGit invokes `git -C dir <args...>` and returns its stdout. If the
// command fails, the returned error includes the captured stderr to aid
// debugging.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// gitenv.Command owns the child environment: a leaked GIT_DIR /
	// GIT_INDEX_FILE outranks `-C dir`, so passing dir alone is not enough to
	// keep this call inside dir. See internal/gitenv (pg2-lx41y).
	cmd := gitenv.Command(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("git %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
