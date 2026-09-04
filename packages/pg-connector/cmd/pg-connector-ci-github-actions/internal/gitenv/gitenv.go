// Package gitenv owns the environment of every child `git`/`gh` process
// this backend spawns.
//
// This is a per-backend copy of cmd/pg-connector-pr-github/internal/gitenv's
// identical package [carry-over basis, per this packet's contract] — Go's
// internal/ visibility rule makes that sibling package unreachable from
// here, and packages/pg-connector's own layout convention
// (cmd/pg-connector/layout_convention_test.go) requires each backend's code
// to live under its own cmd/<binary>/internal/ tree rather than exporting a
// shared non-pkg package [design: §5.2]. The mechanism and rationale are
// identical to the sibling copy's own doc comment: a git/gh invocation that
// names its target directory (`git -C <dir> ...`, `gh --repo <owner/name>`,
// exec.Cmd.Dir) is NOT scoped to that directory, because git resolves
// GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE from the ENVIRONMENT first, ahead of
// any of those. A leaked GIT_DIR (e.g. inherited from a git hook or a linked
// worktree) redirects every descendant git/gh child at the wrong repository
// regardless of -C/--repo/cmd.Dir.
//
// Every git/gh child this backend spawns MUST therefore be built with an
// environment passed through [Hermetic] (or [Environ]) rather than a bare
// os.Environ() passthrough.
package gitenv

import (
	"os"
	"strings"
)

// gitVarPrefix is the namespace this package filters. Everything OUTSIDE it
// is passed through untouched (PATH, HOME, SSH_AUTH_SOCK, proxy vars,
// TMPDIR, locale/XDG dirs, GH_*); everything INSIDE it is dropped unless it
// appears in inheritableGitVars, so an unrecognized GIT_-prefixed variable
// is excluded automatically rather than requiring a denylist to keep up.
const gitVarPrefix = "GIT_"

// inheritableGitVars is the ALLOWLIST of GIT_-prefixed variables a child
// inherits. Membership requires that the variable name no repository,
// index, object store, or discovery boundary — only a program to run or a
// config FILE to read.
//
// Deliberately absent, and therefore dropped: GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_PREFIX, GIT_CEILING_DIRECTORIES,
// GIT_NAMESPACE, GIT_DISCOVERY_ACROSS_FILESYSTEM (all name or bound a
// repository); GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n
// (inline config injection); GIT_AUTHOR_* / GIT_COMMITTER_* (this backend
// never commits); GIT_EXEC_PATH and the GIT_TRACE* family (git recomputes
// the former, and the latter would inject trace output into stderr this
// backend folds into its own error messages).
var inheritableGitVars = map[string]struct{}{
	// Transport: a `gh`/`git` child needs whatever ssh wrapper the operator
	// configured. Each names a PROGRAM, not a repository.
	"GIT_SSH":           {},
	"GIT_SSH_COMMAND":   {},
	"GIT_SSH_VARIANT":   {},
	"GIT_PROXY_COMMAND": {},

	// Interaction policy: dropping these would let a child block on an
	// interactive prompt/editor instead of failing in this non-interactive
	// backend.
	"GIT_ASKPASS":         {},
	"GIT_TERMINAL_PROMPT": {},
	"GIT_EDITOR":          {},

	// WHICH config FILES git reads. A caller (CI, or a test fixture) uses
	// these to sandbox this backend, so forwarding them is a safety feature
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
