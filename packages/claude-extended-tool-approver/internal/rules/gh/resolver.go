package gh

import (
	"context"
	"os"
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

// gitVarPrefix is the namespace hermeticGitEnviron filters. Everything OUTSIDE
// it is passed through untouched (PATH, HOME, SSH_AUTH_SOCK, proxy vars,
// locale, ...); everything INSIDE it is dropped unless it appears in
// inheritableGitVars. Same allowlist-inversion design as pg-pr's
// internal/gitenv (pg2-lx41y): a GIT_-prefixed variable this list has never
// heard of, including one a future git release invents, is excluded
// automatically rather than requiring someone to remember to ban it.
const gitVarPrefix = "GIT_"

// inheritableGitVars is the ALLOWLIST of GIT_-prefixed variables CurrentBranch's
// child inherits. Membership requires that the variable name a PROGRAM to run
// or a config FILE to read — never a repository, index, object store, or
// discovery boundary.
//
// Deliberately absent, and therefore dropped: GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_PREFIX, GIT_CEILING_DIRECTORIES,
// GIT_NAMESPACE, GIT_DISCOVERY_ACROSS_FILESYSTEM (all name or bound a
// repository) — this is exactly the family that outranks `-C <dir>` in git's
// own repository discovery, which is the leak pg2-2pokz (and pg2-lx41y before
// it) is about: a `git commit` from a linked worktree exports GIT_DIR /
// GIT_INDEX_FILE into the hook environment, every descendant inherits them
// unless the child's env is built explicitly, and `git -C <cwd> rev-parse
// --abbrev-ref HEAD` then silently answers with the LEAKED repository's
// checked-out branch instead of cwd's.
var inheritableGitVars = map[string]struct{}{
	"GIT_SSH":             {},
	"GIT_SSH_COMMAND":     {},
	"GIT_SSH_VARIANT":     {},
	"GIT_PROXY_COMMAND":   {},
	"GIT_ASKPASS":         {},
	"GIT_TERMINAL_PROMPT": {},
	"GIT_EDITOR":          {},
	"GIT_CONFIG_GLOBAL":   {},
	"GIT_CONFIG_SYSTEM":   {},
	"GIT_CONFIG_NOSYSTEM": {},
}

// hermeticGitEnviron returns the current process environment with every
// GIT_-prefixed variable removed except those in inheritableGitVars, so the
// `git -C cwd ...` child below cannot be redirected at a different repository
// by an ambient GIT_DIR/GIT_WORK_TREE/etc.
func hermeticGitEnviron() []string {
	ambient := os.Environ()
	out := make([]string, 0, len(ambient))
	for _, kv := range ambient {
		key, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, gitVarPrefix) {
			if _, inherit := inheritableGitVars[key]; !inherit {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

// CurrentBranch returns the checked-out branch for the given working directory.
func (r *ExecBranchResolver) CurrentBranch(cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Env = hermeticGitEnviron()
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
