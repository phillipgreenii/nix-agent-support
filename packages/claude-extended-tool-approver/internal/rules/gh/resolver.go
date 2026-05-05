package gh

import (
	"context"
	"os/exec"
	"strings"
	"time"
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
func (r *ExecBranchResolver) CurrentBranch(cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
