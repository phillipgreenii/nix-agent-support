package gitenv

import (
	"slices"
	"strings"
	"testing"
)

// leakedByAGitHookCommit is the GIT_* set a `git commit` FROM A LINKED
// WORKTREE actually exports into the hook environment (captured 2026-08-27,
// git 2.54.0; pg2-lx41y, ported from the sibling pg-connector-pr-github
// backend's own gitenv_test.go — bead pg2-sxfwd, closing the "ci-side
// copies have zero test files" gap). Every descendant of the hook —
// including a `go test` or this backend launched from it — inherits
// these.
var leakedByAGitHookCommit = []string{
	"GIT_DIR=/canonical/.git/worktrees/wt",
	"GIT_INDEX_FILE=/canonical/.git/worktrees/wt/index",
	"GIT_PREFIX=packages/pg-connector/",
	"GIT_EXEC_PATH=/nix/store/xxx-git/libexec/git-core",
	"GIT_AUTHOR_NAME=Someone",
	"GIT_AUTHOR_EMAIL=someone@example.com",
	"GIT_COMMITTER_NAME=Someone",
	"GIT_COMMITTER_EMAIL=someone@example.com",
}

// mustNotReachAChild lists, beyond the hook set above, every GIT_* variable
// that can redirect a child at a repository, an index, an object store, a
// discovery boundary, or an injected config value.
var mustNotReachAChild = []string{
	"GIT_WORK_TREE=/canonical",
	"GIT_COMMON_DIR=/canonical/.git",
	"GIT_OBJECT_DIRECTORY=/canonical/.git/objects",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=/elsewhere/objects",
	"GIT_CEILING_DIRECTORIES=/",
	"GIT_NAMESPACE=refs/namespaces/x",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
	"GIT_CONFIG=/canonical/.git/config",
	"GIT_CONFIG_COUNT=1",
	"GIT_CONFIG_KEY_0=core.worktree",
	"GIT_CONFIG_VALUE_0=/canonical",
	"GIT_TRACE=1",
	"GIT_TRACE2=1",
	"GIT_TEMPLATE_DIR=/elsewhere/templates",
}

func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// TestHermeticDropsEveryRepoRedirectingVar is the regression test the fix
// exists for: if any of these reaches the git/gh child, `--repo`/cmd.Dir
// is a lie and a mutating verb acts on the wrong repository.
func TestHermeticDropsEveryRepoRedirectingVar(t *testing.T) {
	base := slices.Concat(leakedByAGitHookCommit, mustNotReachAChild)
	got := envKeys(Hermetic(base))
	if len(got) != 0 {
		t.Fatalf("Hermetic leaked %d var(s) that must never reach a git/gh child: %v", len(got), got)
	}
}

// TestHermeticPassesNonGitVarsThrough guards the other half of the
// contract: production git/gh needs the ambient non-GIT_ environment
// (transport, home, locale, temp dir, and this backend's own GH_* auth
// chain), so filtering must not degrade into a full allowlist.
func TestHermeticPassesNonGitVarsThrough(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/Users/someone",
		"SSH_AUTH_SOCK=/tmp/ssh.sock",
		"TMPDIR=/tmp/",
		"LANG=en_US.UTF-8",
		"XDG_CONFIG_HOME=/Users/someone/.config",
		"https_proxy=http://proxy:3128",
		"GH_TOKEN=tok",     // no GIT_ prefix
		"GITHUB_TOKEN=tok", // no GIT_ prefix
	}
	got := Hermetic(base)
	if !slices.Equal(got, base) {
		t.Fatalf("Hermetic altered the non-GIT_ environment\n got: %v\nwant: %v", got, base)
	}
}

func TestHermeticKeepsAllowlistedGitVars(t *testing.T) {
	base := []string{
		"GIT_SSH_COMMAND=ssh -o Foo=bar",
		"GIT_SSH=/usr/bin/ssh",
		"GIT_SSH_VARIANT=ssh",
		"GIT_PROXY_COMMAND=/usr/bin/proxy",
		"GIT_ASKPASS=/usr/bin/askpass",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	got := Hermetic(base)
	if !slices.Equal(got, base) {
		t.Fatalf("Hermetic dropped an allowlisted var\n got: %v\nwant: %v", got, base)
	}
}

// TestHermeticAllowlistNamesNoRepositoryLocation asserts the allowlist's
// membership rule mechanically: nothing in it may end in a location-shaped
// suffix. A future entry that does trips this test instead of shipping.
func TestHermeticAllowlistNamesNoRepositoryLocation(t *testing.T) {
	banned := []string{"_DIR", "_DIRECTORY", "_DIRECTORIES", "_FILE", "_TREE", "_PREFIX", "_NAMESPACE"}
	for k := range inheritableGitVars {
		for _, suffix := range banned {
			if strings.HasSuffix(k, suffix) {
				t.Errorf("allowlisted %q ends in %q: this package's allowlist must name only programs and config files, never a repository location", k, suffix)
			}
		}
	}
}

func TestHermeticDoesNotModifyBase(t *testing.T) {
	base := []string{"PATH=/usr/bin", "GIT_DIR=/canonical/.git", "HOME=/h"}
	snapshot := slices.Clone(base)
	_ = Hermetic(base)
	if !slices.Equal(base, snapshot) {
		t.Fatalf("Hermetic mutated its argument: %v", base)
	}
}

// TestEnvironExcludesLeakedGitDir is this backend's analogue of the
// sibling pg-connector-pr-github backend's TestCommandEnvExcludesLeakedGitDir
// (pg2-lx41y's acceptance test) — this package has no [Command] helper of
// its own (every `git`/`gh` invocation here goes through this backend's own
// ghRunner/gh gateway instead), so [Environ] applied to the current
// process environment is what every child actually inherits, and this
// test exercises that directly via t.Setenv rather than asserting on a
// constructed exec.Cmd.
func TestEnvironExcludesLeakedGitDir(t *testing.T) {
	for _, kv := range leakedByAGitHookCommit {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	got := envKeys(Environ())
	for _, k := range got {
		if strings.HasPrefix(k, "GIT_") {
			if _, inherit := inheritableGitVars[k]; !inherit {
				t.Errorf("Environ() carries %q into a git/gh child", k)
			}
		}
	}
}
