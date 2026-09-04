// gitexec.go: this backend's own thin git-exec wrapper — the same
// small-command-exec-wrapper-with-a-fake-runner-seam shape
// cmd/pg-connector-pr-github/internal/github's CLI/ghexec.go establishes
// for this module, applied to plain `git` instead of `gh`. Unlike that
// GitHub wrapper, this one resolves and injects no credential: local git
// plumbing has no remote credentials concept at all, so there is nothing
// to inject [design: §4.6, §4.7]. It still owns the child's environment,
// via this package's own internal/gitenv (a leaked GIT_DIR/GIT_WORK_TREE
// would otherwise silently redirect `worktree add`/`worktree remove` onto
// the wrong repository regardless of `-C`/cmd.Dir — see that package's
// doc comment).
package internal

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-scm-git/internal/gitenv"
)

// Runner is the seam every Provider method in this package runs its git
// invocations through, so tests can inject a fake instead of spawning a
// real git subprocess for every case. Production code uses execRunner;
// only the end-to-end tests asserting real git behavior (per the packet's
// own AC — at least one method must be exercised against a real git
// checkout, not just mocks) construct a Provider with NewExecRunner.
type Runner interface {
	// Run execs `git <args...>` with its working directory set to dir (an
	// empty dir inherits this process's own cwd — how WorktreeAdd/
	// WorktreeRemove/WorktreeList resolve "the current repository", since
	// none of those three ops carry a repo/cwd wire argument of their own
	// [design: §4.7]; BranchDetect is the one op that instead receives an
	// explicit cwd over the wire and passes it straight through as dir).
	// It returns git's trimmed stdout, or an error folding in stderr.
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// execRunner is the production Runner: shells out to the real `git`
// binary resolved from PATH, via gitenv.Command's hermetic child
// environment.
type execRunner struct{}

// NewExecRunner returns the production, real-git-backed Runner.
func NewExecRunner() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitenv.Command(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderrTxt := strings.TrimSpace(stderr.String()); stderrTxt != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderrTxt)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
