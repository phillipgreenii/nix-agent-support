package patchid

// Guards for hermeticEnviron (patchid_test.go), bead pg2-rxs4j: no variable that
// selects a git repository may reach the git calls of the Client under test,
// whatever the parent process exports.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

var hookLeakEnv = []string{
	"GIT_DIR=/outer/repo/.git",
	"GIT_WORK_TREE=/outer/repo",
	"GIT_INDEX_FILE=/outer/repo/.git/index",
	"GIT_COMMON_DIR=/outer/repo/.git",
	"GIT_OBJECT_DIRECTORY=/outer/repo/.git/objects",
	"GIT_PREFIX=packages/pb/",
	"GIT_CEILING_DIRECTORIES=/outer",
	"GIT_ANYTHING_FUTURE=1",
}

// TestHermeticEnviron_DropsAHookEnvironmentOfTheProcess: a hook-shaped
// environment in the process environment MUST NOT reach the child; the old
// helper removed six enumerated names and fails here on the others.
func TestHermeticEnviron_DropsAHookEnvironmentOfTheProcess(t *testing.T) {
	for _, kv := range hookLeakEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/outer/gitconfig")
	t.Setenv("SOMETHING_ELSE", "1")
	have := map[string]string{}
	for _, kv := range hermeticEnviron() {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s appears twice in the child environment", k)
		}
		have[k] = v
	}
	for _, kv := range hookLeakEnv {
		k, _, _ := strings.Cut(kv, "=")
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the child environment", k, v)
		}
	}
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Error("a name off the allowlist leaked")
	}
	if have["GIT_CONFIG_GLOBAL"] != "/dev/null" || have["GIT_CONFIG_NOSYSTEM"] != "1" || have["GIT_CONFIG_SYSTEM"] != "/dev/null" {
		t.Errorf("git config pins lost or overridden: %v", have)
	}
	if have["PATH"] == "" {
		t.Error("PATH must still reach the child")
	}
}

func foreignGitEnv(repo string) []string {
	return []string{
		"GIT_DIR=" + filepath.Join(repo, ".git"),
		"GIT_WORK_TREE=" + repo,
		"GIT_INDEX_FILE=" + filepath.Join(repo, ".git", "index"),
	}
}

// foreignRepoWithDifferentCommit is another repository whose HEAD commit has
// different content from the repository under test.
func foreignRepoWithDifferentCommit(t *testing.T) string {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pb-patchid-foreign"})
	commit(t, repo, "foreign.txt", "something else entirely\n", "foreign commit")
	return repo.Dir
}

// TestCompute_ForeignGitEnv_StillAnswersForTheRepoItIsGiven runs the real git
// patch-id computation with GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE exported
// to ANOTHER repository: the answer for the repository named by the call must be
// unchanged. Restoring any inheritance of those names makes git read the
// foreign repository instead.
func TestCompute_ForeignGitEnv_StillAnswersForTheRepoItIsGiven(t *testing.T) {
	repo := initRepo(t)
	commit(t, repo, "a.txt", "hello\n", "add a")
	c := Client{R: hermeticCLIRunner{}}
	want, err := c.Compute(context.Background(), repo.Dir, "HEAD")
	if err != nil || want == "" {
		t.Fatalf("Compute before the foreign environment: id=%q err=%v", want, err)
	}

	foreign := foreignRepoWithDifferentCommit(t)
	for _, kv := range foreignGitEnv(foreign) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	got, err := c.Compute(context.Background(), repo.Dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute under the foreign git environment: %v", err)
	}
	if got != want {
		t.Fatalf("patch-id under a foreign GIT_DIR = %q, want %q: the git calls followed the foreign environment", got, want)
	}
}

// leakyRunner is the old behaviour: the foreign selectors reach git.
type leakyRunner struct{ extra []string }

func (l leakyRunner) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	opts.Env = append(hermeticEnviron(), l.extra...)
	return run.CLIRunner{}.Run(ctx, name, args, opts)
}

// TestCompute_ForeignGitEnv_NegativeControl: the SAME environment, handed to a
// git that inherits it, gives a DIFFERENT patch-id (the foreign repository's).
// If git ever stops following GIT_DIR this fails and says so, instead of the
// guard above passing for no reason.
func TestCompute_ForeignGitEnv_NegativeControl(t *testing.T) {
	repo := initRepo(t)
	commit(t, repo, "a.txt", "hello\n", "add a")
	want, err := Client{R: hermeticCLIRunner{}}.Compute(context.Background(), repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	foreign := foreignRepoWithDifferentCommit(t)
	got, err := Client{R: leakyRunner{extra: foreignGitEnv(foreign)}}.Compute(context.Background(), repo.Dir, "HEAD")
	if err == nil && got == want {
		t.Fatalf("a git that inherits GIT_DIR still answered for the repository it was given (%q): the guard above proves nothing (revisit pg2-rxs4j)", got)
	}
}
