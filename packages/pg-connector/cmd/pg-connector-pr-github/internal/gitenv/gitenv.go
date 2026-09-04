// Package gitenv owns the environment of every child `git` process this
// module spawns.
//
// A git invocation that names its target directory — `git -C <dir> ...`, or
// exec.Cmd.Dir, or an explicit repository path argument — is NOT scoped to
// that directory. Git's repository discovery consults the ENVIRONMENT first,
// so GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE outrank all three. Proven
// 2026-08-27 (pg2-lx41y; full mechanism write-up on pg2-67h4y) under a
// leaked GIT_DIR:
//
//	git -C <tmpdir> rev-parse --git-dir        -> the LEAKED repo
//	git -C <tmpdir> rev-parse --show-toplevel  -> <tmpdir>   (looks scoped)
//
// That asymmetry — real git-dir, fake work-tree — is `core.worktree`
// semantics, and it is how the leak gets in: `git commit` FROM A LINKED
// WORKTREE exports GIT_DIR and GIT_INDEX_FILE into the hook environment, and
// every descendant process inherits them. So any pg-pr process launched from
// a git hook (or under `git rebase`, `git bisect run`, or any tool that was
// itself launched from a hook) runs with the leak, and a mutating verb —
// `worktree add`, `worktree remove`, `fetch`, `config` — then acts on the
// leaked repository instead of the directory the caller asked for. In a
// linked worktree `git config` writes to $GIT_COMMON_DIR/config, i.e. the
// canonical clone's config shared by every worktree, which is exactly how
// the canonical clone's own .git/config was corrupted (pg2-12795 /
// pg2-5ek6b). A plain-clone commit hook exports GIT_INDEX_FILE but NOT
// GIT_DIR, which is why a standalone `prek run` never reproduces this and
// only worktree commits do.
//
// The TestMain scrubs added by f04c2389 (see internal/worktree/main_test.go)
// fixed only the TEST side: they stop the test binaries from poisoning a real
// clone, and do nothing for pg-pr running for real in a GIT_DIR-bearing
// environment.
//
// Every `git` child in this module MUST therefore be built by [Command],
// which owns the child environment. A bare
// exec.CommandContext(ctx, "git", ...) with no cmd.Env — or a
// cmd.Env = append(os.Environ(), ...) — is the defect this package exists to
// remove.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// gitVarPrefix is the namespace this package filters. Everything OUTSIDE it
// is passed through untouched: production git needs PATH, HOME,
// SSH_AUTH_SOCK, the proxy vars, TMPDIR, the locale and the XDG dirs to
// function, and dropping them would change behaviour far beyond the defect.
// Everything INSIDE it is dropped unless it appears in inheritableGitVars —
// so a variable this list has never heard of (forgotten today, or invented by
// a future git release) is excluded automatically. That inversion is the
// point: pg2-8wnhc's ruling is that scrubbing a known-bad denylist is a
// partial mitigation, and 5 distinct denylists with 3 different variable sets
// already stand in this workspace.
const gitVarPrefix = "GIT_"

// inheritableGitVars is the ALLOWLIST of GIT_-prefixed variables a child
// inherits. Membership requires that the variable name NO repository, index,
// object store, or discovery boundary — only a program to run or a config
// FILE to read.
//
// Deliberately absent, and therefore dropped: GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_PREFIX, GIT_CEILING_DIRECTORIES,
// GIT_NAMESPACE, GIT_DISCOVERY_ACROSS_FILESYSTEM (all name or bound a
// repository); GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n
// (inline config INJECTION — the route pg2-a12rl and pg2-xjt1s flag, which no
// existing scrub list in this workspace covers); GIT_AUTHOR_* and
// GIT_COMMITTER_* (a hook exports these for the commit in progress; no verb
// here commits, and the target repo's own config is the right identity
// source); and GIT_EXEC_PATH plus the GIT_TRACE* family (git recomputes the
// former from its own location, and the latter would inject trace output into
// the stderr that callers fold into error messages).
var inheritableGitVars = map[string]struct{}{
	// Transport: `fetch` needs whatever ssh wrapper the operator configured.
	// Each names a PROGRAM, not a repository.
	"GIT_SSH":           {},
	"GIT_SSH_COMMAND":   {},
	"GIT_SSH_VARIANT":   {},
	"GIT_PROXY_COMMAND": {},

	// Interaction policy. Dropping GIT_TERMINAL_PROMPT=0 would let a `fetch`
	// against a repo needing credentials block on a prompt instead of
	// failing; dropping GIT_EDITOR would let a future interactive verb open
	// an editor in a non-interactive process.
	"GIT_ASKPASS":         {},
	"GIT_TERMINAL_PROMPT": {},
	"GIT_EDITOR":          {},

	// WHICH config FILES git reads. A caller — CI, or a test fixture — uses
	// these to sandbox pg-pr, so forwarding them is a safety feature rather
	// than a leak, and none of them names a repository. Contrast
	// GIT_CONFIG_COUNT/KEY_n/VALUE_n above, which inject config VALUES and
	// are dropped.
	"GIT_CONFIG_GLOBAL":   {},
	"GIT_CONFIG_SYSTEM":   {},
	"GIT_CONFIG_NOSYSTEM": {},
}

// Hermetic returns base with every GIT_-prefixed variable removed except
// those in inheritableGitVars. base is not modified. It is exported
// separately from [Environ] so it can be exercised without touching the
// process environment.
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

// Command builds `git [-C dir] <args...>` with a hermetic child environment.
// An empty dir omits -C, so the child inherits the parent's working
// directory.
//
// Callers still pass dir: it is necessary (git must be pointed at the right
// place) but, as this package's doc comment explains, not sufficient. Both
// halves are required, which is why they are supplied together here rather
// than left to each call site.
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
