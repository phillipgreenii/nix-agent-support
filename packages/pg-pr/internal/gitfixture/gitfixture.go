// Package gitfixture gives pg-pr's Go tests one hermetically-isolated way to
// shell out to a real `git` binary against a throwaway t.TempDir() fixture
// repository.
//
// Every git-fixture test in this module used to build its own ad hoc
// exec.Command("git", ...) calls, several of them forwarding the FULL
// inherited os.Environ() (or leaving cmd.Env nil, which does the same thing
// implicitly). `-C <dir>` only changes the working directory before git
// runs; it does NOT override GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE,
// GIT_CEILING_DIRECTORIES and friends, which git's own repository discovery
// consults FIRST and which `-C` cannot override. Any of those leaked into
// the environment that launched `go test` -- notably, a git hook's own
// subprocess environment, and this module's `run-unit-tests` pre-commit hook
// IS such a hook -- silently redirects every "isolated" git call in a
// fixture onto whatever repository/worktree that variable names instead of
// the throwaway t.TempDir() fixture. This is confirmed (pg2-12795) as the
// exact mechanism that corrupted the CANONICAL clone's own .git/config with
// `core.worktree=<a linked worktree>` and `user.email`/`user.name` set to
// this package's own test fixture identity (test@example.com / test).
//
// Fixed the same way packages/claude-extended-tool-approver fixed the
// identical defect class (pg2-8wnhc/pg2-rrhw2): by inverting a denylist (
// scrub known-leaky var names) into an ALLOWLIST (forward only the handful
// of vars git demonstrably needs for local, no-network init/config/commit/
// worktree/branch operations). Any git env var this list does not name --
// known today, forgotten today, or invented by a future git release -- is
// excluded automatically, because inclusion requires an explicit entry
// rather than someone remembering to add it to a ban list before it can
// leak. HOME is a second, independent confinement layer: instead of
// forwarding the ambient value, it is pointed at its own fresh t.TempDir().
// GIT_CONFIG_NOSYSTEM=1 skips system config outright, and GIT_CONFIG_GLOBAL/
// GIT_CONFIG_SYSTEM are pinned to /dev/null via t.Setenv so that even a git
// codepath this allowlist has not anticipated finds no config to read.
package gitfixture

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Env returns a minimal, explicitly allowlisted environment for a git
// subprocess exercising one of this module's fixtures, so that no code path
// here can touch a real repository or worktree by construction -- not
// merely because today's known-leaky var names happen to be scrubbed from a
// denylist. See the package doc comment for the full rationale.
func Env(t *testing.T) []string {
	t.Helper()
	// Belt-and-suspenders: even if some git codepath falls back to reading
	// global/system config despite the allowlist below, point both at
	// /dev/null so it finds nothing there either.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	ambient := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			ambient[k] = v
		}
	}
	env := []string{"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	// PATH: to locate the git binary and anything it execs. TMPDIR: git's
	// own scratch files. GIT_CONFIG_GLOBAL/_SYSTEM: forwarded so the
	// t.Setenv overrides above actually reach the subprocess. None of these
	// four names a git repository location.
	for _, k := range []string{"PATH", "TMPDIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if v, ok := ambient[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// CommitEnv is Env plus a fixed test author/committer identity, so a fixture
// can create commits before (or without) a `git config user.name`/
// `user.email` having been set in the fixture repo itself. Setting these
// vars for a non-committing git command (e.g. `remote add`, `update-ref`) is
// a harmless no-op, so every helper in this package uses CommitEnv
// unconditionally rather than exposing two variants.
func CommitEnv(t *testing.T) []string {
	t.Helper()
	return append(
		Env(t),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
}

// Run runs `git -C dir <args...>` with a hermetic CommitEnv and returns its
// trimmed combined stdout+stderr and any error. Use this (rather than
// MustRun) when the caller needs to inspect success/failure itself -- e.g.
// asserting that a command is expected to fail.
func Run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = CommitEnv(t)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// MustRun runs `git -C dir <args...>` with a hermetic CommitEnv and fails the
// test immediately if it errors. It returns the trimmed combined
// stdout+stderr on success.
func MustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Run(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// gitLocationEnvVars are the environment variables that override git's own
// repository discovery, consulted BEFORE `-C <dir>` or the process's cwd
// (git(1), ENVIRONMENT VARIABLES: "Repository", "Path").
var gitLocationEnvVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_PREFIX", "GIT_CEILING_DIRECTORIES",
}

// ScrubProcessGitEnv unsets gitLocationEnvVars from THIS PROCESS's own
// environment. Call it once, early in a package's TestMain (before m.Run()),
// in any package whose tests exercise git subprocess calls -- either
// directly via this package's Run/MustRun (already hermetic per call, see
// Env/CommitEnv above) or, more importantly, PRODUCTION code under test that
// shells out to git with cmd.Env left nil. A nil cmd.Env makes exec inherit
// the process's AMBIENT environment at call time, which Env/CommitEnv cannot
// reach into -- they only protect subprocess calls this package makes
// itself.
//
// This is a DENYLIST, deliberately, unlike Env/CommitEnv: it must leave
// PATH, HOME, TMPDIR and everything else the process legitimately needs
// untouched, so "allowlist everything" is not an option at the whole-process
// level the way it is for a caller-constructed cmd.Env.
//
// Confirmed necessary (pg2-12795): running this module's tests from inside a
// git hook's own subprocess environment -- this repo's `run-unit-tests`
// pre-commit hook IS such a hook -- leaks GIT_DIR/GIT_WORK_TREE into the
// whole `go test` process. Reproduced live: internal/branch.Detect and
// pg-pr's `branch detect` CLI, both invoked (as the SUT under test) against
// an isolated t.TempDir() fixture with a forged remote/branch, instead
// reported the REAL invoking worktree's repo/branch while a
// `run-unit-tests` hook run was in progress -- because branch.Detect's own
// git subprocess calls leave cmd.Env nil and inherited that leaked GIT_DIR/
// GIT_WORK_TREE, which `-C <fixtureDir>` cannot override.
func ScrubProcessGitEnv() {
	for _, k := range gitLocationEnvVars {
		_ = os.Unsetenv(k)
	}
}
