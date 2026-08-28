// Package gitfacet resolves the git-dependent session-location facets reported
// by ccpool list --json (repo root, worktree, branch). Every facet fails SOFT:
// outside a git work tree (or when git is absent / errors) the corresponding
// field is nil, never an error, so a single bad cwd can't fail the whole list.
package gitfacet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Facets are the git-dependent location facets for a cwd. A nil field means the
// facet is unavailable (cwd is not inside a git work tree, or git failed).
type Facets struct {
	// RepoRoot is the MAIN repository root: for a normal checkout it equals
	// Worktree; for a linked worktree it is the directory containing the shared
	// .git (parent of --git-common-dir), NOT the linked worktree root.
	RepoRoot *string
	// Worktree is the working-tree root for cwd (git rev-parse --show-toplevel).
	Worktree *string
	// Branch is the checked-out branch; nil when detached (rev-parse reports
	// "HEAD") or unavailable.
	Branch *string
}

// Resolve returns the git facets for cwd, querying git with -C cwd. It never
// returns an error: any failed sub-query leaves that facet nil. A cwd outside a
// git work tree yields an all-nil Facets.
func Resolve(cwd string) Facets {
	var f Facets

	// Worktree root = the toplevel of the working tree containing cwd. This also
	// serves as the cheap "are we in a repo at all?" probe: if it fails, leave
	// everything nil.
	top, ok := git(cwd, "rev-parse", "--show-toplevel")
	if !ok {
		return f
	}
	f.Worktree = &top

	// Repo root = parent of the shared git common dir. For a normal checkout the
	// common dir is "<root>/.git", so its parent is the worktree root; for a
	// linked worktree it points at the MAIN repo's .git, so its parent is the
	// main repo root (which differs from the linked worktree).
	if commonDir, ok := git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir"); ok {
		root := filepath.Dir(commonDir)
		f.RepoRoot = &root
	}

	// Branch: nil on detached HEAD (rev-parse --abbrev-ref reports the literal
	// "HEAD") so consumers can distinguish "on a branch" from "detached".
	if branch, ok := git(cwd, "rev-parse", "--abbrev-ref", "HEAD"); ok && branch != "HEAD" {
		f.Branch = &branch
	}

	return f
}

// git runs `git -C cwd <args...>` and returns the trimmed stdout. ok is false
// when git is missing, cwd is not a repo, or the command errors.
//
// The child gets a hermetic environment (hermeticEnviron), not the fully
// inherited ambient one: git's own repository discovery consults
// GIT_DIR/GIT_WORK_TREE/etc BEFORE -C, so a leaked value from the process
// environment (e.g. exported into a `git commit` hook run from a linked
// worktree, per pg2-67h4y) would otherwise silently redirect every "-C cwd"
// call here at a different repository -- and ccpool reports on pool/worktree
// state from these facets, so a wrong answer is a real bug (pg2-aqpvr).
func git(cwd string, args ...string) (string, bool) {
	full := append([]string{"-C", cwd}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = hermeticEnviron()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return "", false
	}
	return s, true
}

// gitVarPrefix is the namespace hermeticEnviron filters. Everything OUTSIDE
// it passes through unchanged (PATH, HOME, SSH_AUTH_SOCK, locale, proxy
// vars, ...); everything INSIDE it is dropped unless listed in
// inheritableGitVars.
const gitVarPrefix = "GIT_"

// inheritableGitVars is the ALLOWLIST of GIT_-prefixed variables a git child
// spawned by this package inherits. Membership requires that the variable
// name a PROGRAM to run or a config FILE to read -- never a repository,
// index, object store, or discovery boundary.
//
// Deliberately absent, and therefore dropped: GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_PREFIX, GIT_CEILING_DIRECTORIES --
// exactly the family that outranks `-C <dir>` in git's own repository
// discovery, and the family a `git commit` from a linked worktree exports
// into every descendant of the hook that ran it (mechanism write-up:
// pg2-67h4y; this package's instance: pg2-aqpvr). Same allowlist-inversion
// design already used in this workspace for the identical defect class:
// pg-pr's internal/gitenv (pg2-lx41y) and
// claude-extended-tool-approver's gh.hermeticGitEnviron (pg2-2pokz) -- a
// GIT_-prefixed variable this list has never heard of, including one a
// future git release invents, is excluded automatically rather than
// requiring someone to remember to add it to a denylist.
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

// hermeticEnviron returns the current process environment with every
// GIT_-prefixed variable removed except those in inheritableGitVars, so a
// `git -C cwd ...` child spawned by this package cannot be redirected at a
// different repository by an ambient GIT_DIR/GIT_WORK_TREE/etc.
func hermeticEnviron() []string {
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
