package gh

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

const defaultResolverTimeout = 3 * time.Second

// ExecBranchResolver resolves branch names by shelling out to git and gh.
type ExecBranchResolver struct {
	Timeout time.Duration
}

// NewExecResolver returns an ExecBranchResolver with the default 3s timeout.
func NewExecResolver() *ExecBranchResolver {
	return &ExecBranchResolver{Timeout: defaultResolverTimeout}
}

// CurrentBranch returns the checked-out branch for the given working directory.
//
// Migrated onto x/gitclient (bead pg2-4xfur, design section 4.5 of epic
// pg2-svfbb) off a raw `git -C cwd rev-parse --abbrev-ref HEAD` exec that
// carried its own hand-rolled env allowlist (hermeticGitEnviron /
// inheritableGitVars, pg2-2pokz's fix for pg2-vc5bp). gitclient.Discover
// anchors a *Client at cwd's repository with an environment built from an
// explicit allowlist (PATH/HOME/SSH_AUTH_SOCK only; no GIT_* var is ever
// inherited) rather than the ambient process environment, so the exact leak
// class pg2-2pokz patched here by hand is now closed by construction and the
// local plumbing is deleted rather than carried forward.
//
// DECISION 1 (recorded per pg2-4xfur's acceptance criteria, design section
// 4.2's migration-behavior doc note): the pre-migration call returned the
// literal string "HEAD" on a detached checkout; gitclient.Locator.CurrentBranch
// returns the typed sentinel ErrDetachedHEAD instead. This method maps
// ErrDetachedHEAD back onto the literal "HEAD" string rather than
// propagating a new error type, so CETA's existing gating behavior is
// UNCHANGED: this resolver's only caller is gh.go's `gh run rerun` handler,
// which compares CurrentBranch(cwd) against the target run's branch and,
// today, always falls through to a Refused/"deferred to claude-code" verdict
// on detached HEAD (git rejects "HEAD" as a branch name, so the literal
// string can never equal a real run's branch and the comparison always
// misses) rather than surfacing a resolver error there. Preserving the
// literal keeps that fallthrough — and its message text — identical instead
// of turning a routine detached-HEAD checkout into a new per-rule error.
//
// DECISION 2 (recorded per pg2-4xfur's acceptance criteria; pg2-vc5bp's own
// suggestion, optional to act on): NOT changed to error when cwd's resolved
// anchor differs from the expected repository. That mismatch was only
// reachable through the env-leak vector (a leaked GIT_DIR making `-C cwd`
// silently answer about a different repository) that this migration itself
// closes by construction — gitclient.Discover walks up from cwd through the
// filesystem under its own hermetic environment, so its anchor is always cwd
// or a real ancestor of it, never a repository selected by leaked
// environment state. Adding an anchor-mismatch check here would guard a
// state gitclient no longer makes representable, so CurrentBranch resolves
// via Discover(ctx, cwd) with no additional check.
func (r *ExecBranchResolver) CurrentBranch(cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	client, err := gitclient.Discover(ctx, cwd)
	if err != nil {
		return "", err
	}
	branch, err := client.CurrentBranch(ctx)
	if err != nil {
		if errors.Is(err, gitclient.ErrDetachedHEAD) {
			return "HEAD", nil
		}
		return "", err
	}
	return branch, nil
}

// RunBranch returns the headBranch of a GitHub Actions workflow run.
func (r *ExecBranchResolver) RunBranch(runID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "run", "view", runID, "--json", "headBranch", "-q", ".headBranch")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
