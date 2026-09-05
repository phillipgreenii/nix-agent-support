package worktree

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/phillipgreenii/x/gitclient"
)

// GitClient abstracts the git operations the worktree package needs.
// The default implementation shells out to the `git` CLI (via x/gitclient);
// tests inject fakes.
type GitClient interface {
	// RepoFromRemote parses `git -C dir config --get remote.origin.url`
	// and returns the GitHub owner/repo it points to.
	RepoFromRemote(ctx context.Context, dir string) (owner, repo string, err error)

	// FetchPR fetches refs/pull/<pr>/head into refs/remotes/origin/pr/<pr>.
	FetchPR(ctx context.Context, dir string, pr int) error

	// RefExists reports whether ref resolves to a commit in dir (e.g.
	// "origin/pr/12"). Used to skip a redundant fetch when the PR head has
	// already been fetched (by the daemon's pre-fetch gate or an earlier run).
	// Returns (false, nil) on any error — a false negative is safe (the
	// caller just fetches anyway).
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

// gitRepo is the composite role set this package's git plumbing needs from
// x/gitclient (design §4.5's consumer mapping: "pg-pr worktree -> composes
// Locator+RefReader+StatusReader+Fetcher+WorktreeManager+BranchManager;
// domain methods (RepoFromRemote, FetchPR, WorktreeInfo) stay local, built
// on these").
type gitRepo interface {
	gitclient.Locator
	gitclient.RefReader
	gitclient.StatusReader
	gitclient.Fetcher
	gitclient.WorktreeManager
	gitclient.BranchManager
}

// opener anchors a gitclient at dir, sized to the widest role set this
// package needs (design §4.6's app-local opener seam for multi-directory
// consumers). Unlike branch.go/gitlocal.go, this package cannot use one
// shared package-level Client: WorktreeInfo(path) and List's per-entry scan
// probe arbitrary paths, and CreateWorktree/FetchPR/RepoFromRemote anchor at
// whatever repoDir the caller supplies, so every call opens its own client.
type opener func(ctx context.Context, dir string) (gitRepo, error)

// openGit is a package-level var, not a plain function, so tests can
// substitute a fake opener (map-backed, or one that fails) without
// threading a new testing seam through GitClient/CLIGitClient itself. This
// mirrors pr-pool's internal/worktree.Opener and internal/watchdog's
// gitOpener.
var openGit opener = func(ctx context.Context, dir string) (gitRepo, error) {
	return gitclient.New(ctx, dir)
}

// CLIGitClient invokes the system `git` binary via x/gitclient.
type CLIGitClient struct{}

// NewCLIGitClient returns a GitClient backed by the system `git` binary.
func NewCLIGitClient() GitClient { return &CLIGitClient{} }

var ghRemoteRE = regexp.MustCompile(`github\.com[:/]([^/]+)/(.+?)(?:\.git)?$`)

func (g *CLIGitClient) RepoFromRemote(ctx context.Context, dir string) (string, string, error) {
	client, err := openGit(ctx, dir)
	if err != nil {
		return "", "", err
	}
	url, err := client.RemoteURL(ctx, "origin")
	if err != nil {
		return "", "", err
	}
	url = strings.TrimSpace(url)
	m := ghRemoteRE.FindStringSubmatch(url)
	if m == nil {
		return "", "", fmt.Errorf("remote.origin.url is not a github URL: %q", url)
	}
	return m[1], m[2], nil
}

func (g *CLIGitClient) FetchPR(ctx context.Context, dir string, pr int) error {
	client, err := openGit(ctx, dir)
	if err != nil {
		return err
	}
	// Force-update (+) and disable prune (the zero-value FetchOptions.AllowPrune
	// default): dir may already have refs/remotes/origin/pr/<pr> from a prior
	// fetch (this call re-fetches on every re-review), and without both of
	// these git's default prune pass deletes that tracking ref (it doesn't
	// match the default refs/heads/*:refs/remotes/origin/* fetch refspec's
	// source side) and then fails to recreate it — see CLIPRFetcher.FetchPRHead
	// in internal/sync/prefetch.go, whose refspec this mirrors exactly.
	refspec := fmt.Sprintf("+pull/%d/head:refs/remotes/origin/pr/%d", pr, pr)
	// Fetch now streams (x/gitclient bead pg2-f1cq7): it returns a *Handle
	// for an already-started invocation rather than blocking. This package
	// has no live-progress UI to attach, so it just waits for completion —
	// the same buffered shape x's own migrated consumers and tests use
	// (see gitclient_test.waitHandle): h, err := Fetch(...); if err == nil
	// { err = h.Wait() }. The Handle is otherwise safe to drop: per its doc
	// comment it self-reaps its process unconditionally, whether or not
	// Wait is ever called.
	h, err := client.Fetch(ctx, gitclient.FetchOptions{Remote: "origin", Refspec: refspec})
	if err != nil {
		return err
	}
	return h.Wait()
}

func (g *CLIGitClient) RefExists(ctx context.Context, dir, ref string) (bool, error) {
	client, err := openGit(ctx, dir)
	if err != nil {
		return false, nil
	}
	// Treat every error (not just "ref does not exist") as "not present" —
	// matches the previous runGit-based behavior, where any non-zero exit
	// from `rev-parse --verify --quiet` collapsed to (false, nil).
	exists, err := client.RefExists(ctx, ref)
	if err != nil {
		return false, nil
	}
	return exists, nil
}

func (g *CLIGitClient) CreateWorktree(ctx context.Context, dir, target, branch, startPoint string) error {
	client, err := openGit(ctx, dir)
	if err != nil {
		return err
	}
	// CreateWorktree streams for the same reason Fetch does (x/gitclient
	// bead pg2-f1cq7); same buffered wait-and-discard shape as FetchPR
	// above — no live-progress UI here, and the Handle self-reaps on its
	// own regardless.
	h, err := client.CreateWorktree(ctx, target, branch, gitclient.CreateWorktreeOptions{StartPoint: startPoint})
	if err != nil {
		return err
	}
	return h.Wait()
}

func (g *CLIGitClient) RemoveWorktree(ctx context.Context, dir, target string, force bool) error {
	client, err := openGit(ctx, dir)
	if err != nil {
		return err
	}
	return client.RemoveWorktree(ctx, target, force)
}

func (g *CLIGitClient) PruneWorktrees(ctx context.Context, dir string) error {
	client, err := openGit(ctx, dir)
	if err != nil {
		return err
	}
	return client.PruneWorktrees(ctx)
}

func (g *CLIGitClient) DeleteBranch(ctx context.Context, dir, branch string, force bool) error {
	client, err := openGit(ctx, dir)
	if err != nil {
		return err
	}
	return client.DeleteBranch(ctx, branch, force)
}

func (g *CLIGitClient) BranchAheadOfRef(ctx context.Context, dir, branch, ref string) (bool, error) {
	client, err := openGit(ctx, dir)
	if err != nil {
		return false, err
	}
	n, err := client.CommitsAhead(ctx, ref, branch)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (g *CLIGitClient) WorktreeInfo(ctx context.Context, path string) (*Worktree, error) {
	client, err := openGit(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Branch name. CurrentBranch (`branch --show-current`) returns
	// gitclient.ErrDetachedHEAD when HEAD does not point at a branch; the
	// previous `rev-parse --abbrev-ref HEAD` returned the literal string
	// "HEAD" in that case (design §4.2's migration behavior note (b)), so
	// that sentinel is mapped back to "HEAD" here to preserve behavior.
	branch, err := client.CurrentBranch(ctx)
	if err != nil {
		if errors.Is(err, gitclient.ErrDetachedHEAD) {
			branch = "HEAD"
		} else {
			return nil, fmt.Errorf("current branch: %w", err)
		}
	}

	// Uncommitted changes.
	entries, err := client.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	hasChanges := len(entries) > 0

	// Unpushed commits (0 if no upstream).
	unpushed := 0
	if hasUpstream, err := client.HasUpstream(ctx); err == nil && hasUpstream {
		if n, err := client.CommitsAhead(ctx, "@{u}", "HEAD"); err == nil {
			unpushed = n
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
