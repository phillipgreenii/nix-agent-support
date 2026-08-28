package gitenv

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// leakedByAGitHookCommit is the GIT_* set a `git commit` FROM A LINKED
// WORKTREE actually exports into the hook environment (captured 2026-08-27,
// git 2.54.0; pg2-lx41y). Every descendant of the hook — including a `go
// test` or a `pr-pool` launched from it — inherits these, and `-C dir` does
// not override any of them.
var leakedByAGitHookCommit = []string{
	"GIT_DIR=/canonical/.git/worktrees/wt",
	"GIT_INDEX_FILE=/canonical/.git/worktrees/wt/index",
	"GIT_PREFIX=packages/pr-pool/",
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
// exists for: if any of these reaches the git child, `-C dir` is a lie and a
// mutating verb acts on the wrong repository.
func TestHermeticDropsEveryRepoRedirectingVar(t *testing.T) {
	base := slices.Concat(leakedByAGitHookCommit, mustNotReachAChild)
	got := envKeys(Hermetic(base))
	if len(got) != 0 {
		t.Fatalf("Hermetic leaked %d var(s) that must never reach a git child: %v", len(got), got)
	}
}

// TestHermeticPassesNonGitVarsThrough guards the other half of the contract:
// production git needs the ambient non-GIT_ environment (transport, home,
// locale, temp dir), so filtering must not degrade into a full allowlist.
func TestHermeticPassesNonGitVarsThrough(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/Users/someone",
		"SSH_AUTH_SOCK=/tmp/ssh.sock",
		"TMPDIR=/tmp/",
		"LANG=en_US.UTF-8",
		"XDG_CONFIG_HOME=/Users/someone/.config",
		"https_proxy=http://proxy:3128",
		"GITHUB_TOKEN=tok", // no GIT_ prefix: GITHUB_, not GIT_
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

// TestCommandEnvExcludesLeakedGitDir is the acceptance test named on
// pg2-bh09g: it asserts on the constructed exec.Cmd.Env, so it needs no
// subprocess and no real repository.
func TestCommandEnvExcludesLeakedGitDir(t *testing.T) {
	for _, kv := range leakedByAGitHookCommit {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	cmd := Command(context.Background(), "/some/target/dir", "status", "--porcelain")

	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil: a nil Env makes the child inherit the parent environment, which is the defect")
	}
	for _, k := range envKeys(cmd.Env) {
		if strings.HasPrefix(k, "GIT_") {
			if _, inherit := inheritableGitVars[k]; !inherit {
				t.Errorf("cmd.Env carries %q into the git child", k)
			}
		}
	}
}

func TestCommandBuildsDirScopedArgs(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		args []string
		want []string
	}{
		{
			name: "dir given",
			dir:  "/target",
			args: []string{"rev-parse", "--abbrev-ref", "HEAD"},
			want: []string{"git", "-C", "/target", "rev-parse", "--abbrev-ref", "HEAD"},
		},
		{
			name: "empty dir omits -C",
			dir:  "",
			args: []string{"status"},
			want: []string{"git", "status"},
		},
		{
			name: "no args",
			dir:  "/target",
			args: nil,
			want: []string{"git", "-C", "/target"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := Command(context.Background(), tc.dir, tc.args...)
			// Args[0] is the resolved path to the git binary; compare on the
			// base name so the assertion holds wherever git lives.
			got := slices.Clone(cmd.Args)
			got[0] = "git"
			if !slices.Equal(got, tc.want) {
				t.Fatalf("args\n got: %v\nwant: %v", got, tc.want)
			}
		})
	}
}
