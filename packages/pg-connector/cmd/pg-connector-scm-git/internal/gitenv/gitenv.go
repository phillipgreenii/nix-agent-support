// Package gitenv owns the environment of every child `git` process this
// backend spawns.
//
// A git invocation that names its target directory — `git -C <dir> ...`, or
// exec.Cmd.Dir — is NOT scoped to that directory. Git's repository
// discovery consults the ENVIRONMENT first, so a leaked
// GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE outranks `-C`/cmd.Dir. This is a
// live, previously-hit defect class in this exact module tree — see this
// module's own cmd/pg-connector-pr-github/internal/gitenv (the sibling
// Tier-2 backend packet's identical package, ported from pg-pr) for the
// full mechanism write-up and provenance. It matters MORE here than there:
// every one of this backend's own git invocations is a direct exec (there
// is no `gh` indirection to go through), and two of its four ops are
// exactly the mutating verbs that write-up names as the ones that
// previously corrupted a canonical clone's own .git/config when run under
// a leaked GIT_DIR — `worktree add` and `worktree remove`.
//
// This package is its own copy, not an import of the sibling backend's:
// Go's internal/ visibility is per-import-path, so
// cmd/pg-connector-pr-github/internal/gitenv is importable only from
// within cmd/pg-connector-pr-github/... — this backend's own
// cmd/pg-connector-scm-git/internal/gitenv is the independent,
// compiler-enforced boundary the design's own layout convention expects
// each backend to have [design: §5.2].
//
// Every `git` child this backend spawns MUST therefore be built by
// [Command], which owns the child environment. A bare
// exec.CommandContext(ctx, "git", ...) with no cmd.Env is the defect this
// package exists to remove.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// gitVarPrefix is the namespace this package filters. Everything OUTSIDE
// it is passed through untouched: production git needs PATH, HOME,
// SSH_AUTH_SOCK, the proxy vars, TMPDIR, the locale and the XDG dirs to
// function. Everything INSIDE it is dropped unless it appears in
// inheritableGitVars, so a variable this list has never heard of is
// excluded automatically.
const gitVarPrefix = "GIT_"

// inheritableGitVars is the ALLOWLIST of GIT_-prefixed variables a child
// inherits. Membership requires that the variable name NO repository,
// index, object store, or discovery boundary — only a program to run or a
// config FILE to read.
//
// Deliberately absent, and therefore dropped: GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_PREFIX, GIT_CEILING_DIRECTORIES,
// GIT_NAMESPACE, GIT_DISCOVERY_ACROSS_FILESYSTEM (all name or bound a
// repository); GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n
// (inline config injection); GIT_AUTHOR_* and GIT_COMMITTER_* (no op this
// backend implements ever commits, so there is no in-progress commit
// identity to forward — unlike a hook environment, which exports these
// for the commit it's running inside of); and GIT_EXEC_PATH plus the
// GIT_TRACE* family (git recomputes the former from its own location, and
// the latter would inject trace output into the stderr this backend folds
// into its own error messages).
var inheritableGitVars = map[string]struct{}{
	// Transport: not strictly needed by this backend's own four ops (none
	// of them fetch/push), but allowlisted for the same reason the sibling
	// package does: each names a PROGRAM, not a repository, so forwarding
	// it carries none of the leak risk this package exists to close.
	"GIT_SSH":           {},
	"GIT_SSH_COMMAND":   {},
	"GIT_SSH_VARIANT":   {},
	"GIT_PROXY_COMMAND": {},

	// Interaction policy. Dropping GIT_TERMINAL_PROMPT=0 would let a
	// command needing credentials block on a prompt instead of failing;
	// dropping GIT_EDITOR would let a future interactive verb open an
	// editor in this non-interactive backend.
	"GIT_ASKPASS":         {},
	"GIT_TERMINAL_PROMPT": {},
	"GIT_EDITOR":          {},

	// WHICH config FILES git reads. A caller — a test fixture — uses these
	// to sandbox this backend, so forwarding them is a safety feature
	// rather than a leak, and none of them names a repository.
	"GIT_CONFIG_GLOBAL":   {},
	"GIT_CONFIG_SYSTEM":   {},
	"GIT_CONFIG_NOSYSTEM": {},
}

// Hermetic returns base with every GIT_-prefixed variable removed except
// those in inheritableGitVars. base is not modified.
func Hermetic(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
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

// Environ is [Hermetic] applied to the current process environment.
func Environ() []string { return Hermetic(os.Environ()) }

// Command builds `git [-C dir] <args...>` with a hermetic child
// environment. An empty dir omits -C, so the child inherits the parent's
// working directory.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if dir != "" {
		full = append(full, "-C", dir)
	}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = Environ()
	return cmd
}
