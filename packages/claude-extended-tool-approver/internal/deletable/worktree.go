package deletable

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WORKTREE-STATE POLICY (tc-lc8f item 4a, resume bead tc-lc8f, design bead
// tc-z806) — slice 3l's workspace declarations (workspace.go) gave every
// entry under a `.worktrees` dir (git kind) or a pn workspace's declared
// workforests_dir (pn kind) an unconditional CatProtected opinion, so any
// delete of a worktree root was Forbidden regardless of the worktree's own
// state. This file supersedes that for the WORKTREE ROOT ITSELF (only):
//
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on bead tc-vn5z):
// "removing a worktree is fine, assuming it osnt dirty. well, a completely
// clean one can be approved. use rejext if there are dirtt workspace.
// abstain for if clean but ignored files exist."
//
// Read as (typos corrected, meaning unambiguous from context): a completely
// clean worktree may be approved; a dirty one must be rejected; a worktree
// that is clean except for ignored files abstains. A worktree whose state
// cannot be determined at all (not actually a worktree, git unavailable, an
// error) is also Unknown — never treated as clean.
//
// `.git/` itself (the primary/canonical repository's own admin directory —
// always a DIRECTORY, never this file's concern) stays Protected exactly as
// workspace.go declares; so does the `.worktrees` container directory itself
// and any path INSIDE a worktree that is not the worktree's own root (slice
// 3l's declarations keep deciding those — see AtWorktreeRoot's doc comment
// for why only the exact root entry is intercepted here).

// WorktreeState is a worktree root's cleanliness, per ProbeWorktreeState.
type WorktreeState int

const (
	// WorktreeUnknown: the state could not be determined — the path is not
	// actually a git worktree, git is unavailable, or the probe itself
	// errored (including a path that no longer exists on disk). Never
	// treated as clean; the zero value, so a zeroed WorktreeState never
	// silently reads as approved.
	WorktreeUnknown WorktreeState = iota
	// WorktreeClean: no tracked modifications, no staged changes, no
	// untracked files, no ignored files either.
	WorktreeClean
	// WorktreeDirty: tracked modifications, staged changes, or untracked
	// non-ignored files are present.
	WorktreeDirty
	// WorktreeCleanIgnored: otherwise clean, but ignored files are present.
	WorktreeCleanIgnored
)

// String returns the deterministic state name.
func (s WorktreeState) String() string {
	switch s {
	case WorktreeClean:
		return "clean"
	case WorktreeDirty:
		return "dirty"
	case WorktreeCleanIgnored:
		return "clean-ignored"
	default:
		return "unknown"
	}
}

// worktreeStateProbe is the injection seam: the golden harness's fixture
// `.git` is a plain directory (never a real repository) and must not start
// running real git, and this package's own unit tests substitute a fake for
// the same reason a production code path must not touch a real repo just to
// exercise the dirty/ignored branches deterministically. Tests substitute a
// fake via SetWorktreeStateProbe; production leaves it at realWorktreeState.
var worktreeStateProbe = realWorktreeState

// ProbeWorktreeState reports root's WorktreeState via the current probe
// (realWorktreeState in production, a substituted fake under test). root
// must be an absolute, already-resolved path — callers (the DeleteAccess
// policy) resolve it with the same patheval.PathEvaluator used for
// everything else before calling this.
func ProbeWorktreeState(root string) (WorktreeState, error) {
	return worktreeStateProbe(root)
}

// SetWorktreeStateProbe substitutes fn as the worktree-state probe for the
// duration of a test and returns a restore func. Exported so both this
// package's own tests (worktree_test.go, against real throwaway repos) and
// internal/effectpolicy's golden/agreement harness (a different package,
// whose fixture's `.git` is not a real repository) can install a
// deterministic fake without a second exported surface.
func SetWorktreeStateProbe(fn func(root string) (WorktreeState, error)) (restore func()) {
	prev := worktreeStateProbe
	worktreeStateProbe = fn
	return func() { worktreeStateProbe = prev }
}

// hermeticGitEnviron builds the environment for a git subprocess this
// package invokes against a caller-given directory: os.Environ() with every
// GIT_* variable stripped, plus GIT_CONFIG_NOSYSTEM=1 and
// GIT_CONFIG_GLOBAL=/dev/null. Mirrors gitignore_test.go's TestGitignoreAgainstGit
// gitCmd helper (read first for this slice), which strips GIT_* for the same
// reason: a GIT_DIR/GIT_INDEX_FILE/GIT_WORK_TREE inherited from an enclosing
// git-hook context (this repo's own pre-commit run, an agent session
// launched from inside one) would silently redirect `-C <root>` at a
// DIFFERENT repository than the one this function was asked to probe. This
// is production code (unlike the test helpers it mirrors), so both this
// function's own callers and this package's unit tests go through it —
// nothing in this package builds a second, divergent git environment.
func hermeticGitEnviron() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

// realWorktreeState runs `git -C root status --porcelain=v1 --ignored`
// hermetically and classifies the output: any non-`!!`-prefixed line means
// dirty (tracked modification, staged change, or untracked file); a `!!`
// line with nothing else means clean-but-ignored; no output at all means
// clean. A non-zero exit (root is not a git repository, git is not on
// PATH, root does not exist) reports WorktreeUnknown with the error —
// never a crash, and never treated as clean.
func realWorktreeState(root string) (WorktreeState, error) {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain=v1", "--ignored")
	cmd.Env = hermeticGitEnviron()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return WorktreeUnknown, &worktreeProbeError{root: root, out: strings.TrimSpace(string(out)), err: err}
	}
	dirty, ignored := false, false
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "!!") {
			ignored = true
			continue
		}
		dirty = true
	}
	switch {
	case dirty:
		return WorktreeDirty, nil
	case ignored:
		return WorktreeCleanIgnored, nil
	default:
		return WorktreeClean, nil
	}
}

// worktreeProbeError carries enough context for a policy Reason string
// without leaking the full (possibly large) git output.
type worktreeProbeError struct {
	root string
	out  string
	err  error
}

func (e *worktreeProbeError) Error() string {
	if e.out == "" {
		return "git status -C " + e.root + ": " + e.err.Error()
	}
	return "git status -C " + e.root + ": " + e.err.Error() + " (" + e.out + ")"
}

func (e *worktreeProbeError) Unwrap() error { return e.err }

// GIT-TRACKED PROBE (tc-lc8f item 3z; deletable.go's "# NON-SECRET
// declarations" doc comment carries the two operator rulings this
// implements) — gitKind.Secrecy's mechanism (workspace.go): "tracked in the
// index" for the git kind's NON-SECRET declaration, following the exact
// same injection-seam shape as ProbeWorktreeState/SetWorktreeStateProbe
// above (worktreeStateProbe), for the identical reason: the golden/
// agreement harness's fixture `.git` is a plain directory, never a real
// repository, and must not start a real git process; this package's own
// unit tests substitute a fake for the same reason production code must
// not touch a real repo just to exercise a deterministic scenario.

// gitTrackedProbe is the injection seam: production leaves it at
// realGitTracked; tests substitute a fake via SetGitTrackedProbe.
var gitTrackedProbe = realGitTracked

// SetGitTrackedProbe substitutes fn as the git-tracked probe for the
// duration of a test and returns a restore func, mirroring
// SetWorktreeStateProbe's contract exactly. Exported so both this
// package's own tests (against real throwaway repositories) and
// internal/effectpolicy's golden/agreement harness can install a
// deterministic fake without a second exported surface.
func SetGitTrackedProbe(fn func(root, rel string, isDir bool) (bool, error)) (restore func()) {
	prev := gitTrackedProbe
	gitTrackedProbe = fn
	return func() { gitTrackedProbe = prev }
}

// realGitTracked reports whether rel (root-relative, slash-separated) is
// tracked in root's git index, run hermetically (hermeticGitEnviron):
//
//   - a FILE operand (isDir false): `git -C root ls-files --error-unmatch
//     -- rel`. Exit 0 means tracked. `--error-unmatch` makes git exit 1
//     specifically when rel names no tracked file — an ORDINARY, expected
//     outcome ("not tracked"), not a probe failure, and is reported as
//     (false, nil).
//   - a DIRECTORY operand (isDir true): `git -C root ls-files -- rel`
//     (no `--error-unmatch`, which git rejects for a directory pathspec
//     with no per-entry match semantics anyway) — non-empty output means
//     at least one file under rel is tracked.
//
// Any OTHER failure (git not on PATH, root not actually a git repository
// despite the workspace declaration's marker match, a killed process) is
// returned as a genuine error, distinct from "exit 1, not tracked" — so
// gitKind.Secrecy's caller (NonSecretWith) can tell "confidently untracked"
// apart from "the question could not be asked" and treat the latter as NO
// OPINION rather than accidentally as "not tracked". This is exactly the
// property realWorktreeState's own doc comment states for its non-zero
// exit: "never treated as clean" there, "never treated as untracked" here.
func realGitTracked(root, rel string, isDir bool) (bool, error) {
	args := []string{"-C", root, "ls-files"}
	if !isDir {
		args = append(args, "--error-unmatch")
	}
	args = append(args, "--", rel)
	cmd := exec.Command("git", args...)
	cmd.Env = hermeticGitEnviron()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, &gitTrackedProbeError{root: root, rel: rel, err: err}
	}
	if isDir {
		return strings.TrimSpace(string(out)) != "", nil
	}
	return true, nil
}

// gitTrackedProbeError carries enough context for a policy Reason string
// without leaking full git output, mirroring worktreeProbeError.
type gitTrackedProbeError struct {
	root, rel string
	err       error
}

func (e *gitTrackedProbeError) Error() string {
	return "git ls-files -C " + e.root + " -- " + e.rel + ": " + e.err.Error()
}

func (e *gitTrackedProbeError) Unwrap() error { return e.err }

// IsWorktreeRoot reports whether abs is a git worktree root by the
// RELIABLE, location-independent signal: a `.git` entry directly under abs
// that is a regular FILE (not a directory) whose content begins with
// `gitdir:` — exactly what `git worktree add` writes, and never what a
// canonical/primary clone's `.git` (always a directory) looks like. abs
// need not be under a `.worktrees` dir or a pn workforests_dir at all; see
// AtWorktreeRoot for the combined check the DeleteAccess policy actually
// uses.
func IsWorktreeRoot(abs string) bool {
	p := filepath.Join(abs, ".git")
	info, err := os.Lstat(p)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(b)), "gitdir:")
}

// IsDeclaredWorktreeSlot reports whether abs is a direct child of a
// `.worktrees` directory (the git kind's convention, workspace.go) or of a
// pn workspace's declared workforests_dir (the pn kind's convention,
// pnWorkforestsDir) — the LOCATION-level signal from slice 3l, independent
// of whether abs currently has the .git FILE marker IsWorktreeRoot checks.
// A slot that is not (or is no longer) an actual worktree still routes
// through worktree-state judgment rather than silently falling back to a
// blanket Protected/Deletable verdict — ProbeWorktreeState reports
// WorktreeUnknown for it, same as for any other path git does not
// recognize as a repository.
func IsDeclaredWorktreeSlot(abs string) bool {
	parent := filepath.Dir(abs)
	if parent == abs {
		return false
	}
	if filepath.Base(parent) == ".worktrees" {
		return true
	}
	grandparent := filepath.Dir(parent)
	if grandparent == parent {
		return false
	}
	if _, err := os.Stat(filepath.Join(grandparent, "pn-workspace.toml")); err != nil {
		return false
	}
	return filepath.Base(parent) == pnWorkforestsDir(grandparent)
}

// AtWorktreeRoot reports whether abs must be judged by worktree state
// (ProbeWorktreeState) rather than by the workspace kinds' Protected/
// Deletable/Keep categories: it is a worktree root by EITHER signal above —
// the reliable `.git`-FILE marker (IsWorktreeRoot), or the declaration-level
// `.worktrees`/workforests_dir location (IsDeclaredWorktreeSlot). Both must
// work per the design brief: a worktree added outside the `.worktrees`
// convention is still caught by the marker; a slot that has lost or never
// had its `.git` file is still caught by location, and resolves to Unknown
// rather than silently reverting to Protected.
//
// A path that is NEITHER — including `.git` itself (always a directory for
// the primary clone; the entry name `.git`, not a directory CONTAINING one,
// so IsWorktreeRoot(".git") asks for "./.git/.git" and is false), the
// `.worktrees` container directory itself (its own parent is not named
// `.worktrees`), and any path nested two or more levels inside a worktree —
// falls through to the pre-existing deletable.Classify/Resolve path
// unchanged (item 2 of this slice's brief: only the worktree ROOT, as a
// unit, is affected).
func AtWorktreeRoot(abs string) bool {
	return IsWorktreeRoot(abs) || IsDeclaredWorktreeSlot(abs)
}
