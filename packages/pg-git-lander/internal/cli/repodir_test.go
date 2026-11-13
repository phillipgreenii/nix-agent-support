package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cli"
)

// otherWorktree adds a branch and a worktree to repo (a repository other
// than f's, or a clone) and returns the worktree path.
func (f *fx) otherWorktree(repo *gitfixture.Repo, name string) string {
	f.t.Helper()
	p := filepath.Join(repo.Root(), name)
	f.git(repo.Dir, "worktree", "add", "-q", "-b", "other/"+name, p)
	return p
}

func (f *fx) usageMsg(r result) string {
	f.t.Helper()
	m := f.jsonOutcome(r)
	if r.code != 2 || m["outcome"] != "usage-error" {
		f.t.Fatalf("want a usage error: %+v", r)
	}
	s, _ := m["message"].(string)
	return s
}

func TestLandFromANonGitCwdUsesTheRepoOfTheWorktreeFlag(t *testing.T) {
	f := newFx(t)
	f.feature()
	r := f.cli(t.TempDir(), "land", "feature/x", "--source-worktree", f.wt, "--json")
	if m := f.jsonOutcome(r); r.code != 0 || m["outcome"] != "landed" {
		t.Fatalf("%+v", r)
	}
}

func TestRebaseFromANonGitCwdUsesTheRepoOfTheWorktreeFlag(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.commit(f.repo.Dir, "m.txt", "m\n")
	r := f.cli(t.TempDir(), "rebase", "feature/x", "--onto", f.main, "--work-worktree", f.wt, "--json")
	if m := f.jsonOutcome(r); r.code != 0 || m["outcome"] != "rebased" {
		t.Fatalf("%+v", r)
	}
}

func TestLandFromANonGitCwdUsesThePGLVariable(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.env[cli.EnvSourceWorktree] = f.wt
	r := f.cli(t.TempDir(), "land", "feature/x", "--json")
	if m := f.jsonOutcome(r); r.code != 0 || m["outcome"] != "landed" {
		t.Fatalf("%+v", r)
	}
}

func TestRepoFlagThatAgreesWithTheWorktreeFlagWins(t *testing.T) {
	f := newFx(t)
	f.feature()
	r := f.cli(t.TempDir(), "land", "feature/x", "--repo", f.repo.Dir, "--source-worktree", f.wt, "--json")
	if m := f.jsonOutcome(r); r.code != 0 || m["outcome"] != "landed" {
		t.Fatalf("%+v", r)
	}
}

func TestWorktreeFlagsInDifferentReposAreRefused(t *testing.T) {
	f := newFx(t)
	f.feature()
	sib, err := f.repo.NewSibling(t.Context(), "beta", gitfixture.RepoOptions{InitialCommit: true})
	if err != nil {
		t.Fatal(err)
	}
	other := f.otherWorktree(sib, "wt-beta")
	r := f.cli(t.TempDir(), "land", "feature/x", "--source-worktree", f.wt, "--target-worktree", other, "--json")
	msg := f.usageMsg(r)
	if !strings.Contains(msg, f.wt) || !strings.Contains(msg, other) || !strings.Contains(msg, "one git clone") {
		t.Fatalf("message does not name both paths: %q", msg)
	}
	if f.git(f.repo.Dir, "rev-parse", "--verify", "refs/heads/feature/x") == "" {
		t.Fatal("branch gone")
	}
}

func TestRepoFlagInAnotherRepoThanTheWorktreeFlagIsRefused(t *testing.T) {
	f := newFx(t)
	f.feature()
	sib, err := f.repo.NewSibling(t.Context(), "beta", gitfixture.RepoOptions{InitialCommit: true})
	if err != nil {
		t.Fatal(err)
	}
	msg := f.usageMsg(f.cli(t.TempDir(), "land", "feature/x", "--repo", sib.Dir, "--source-worktree", f.wt, "--json"))
	if !strings.Contains(msg, sib.Dir) || !strings.Contains(msg, f.wt) {
		t.Fatalf("%q", msg)
	}
}

func TestWorktreeFlagsInTwoClonesOfOneRepoAreRefused(t *testing.T) {
	f := newFx(t)
	f.feature()
	cl, err := f.repo.Clone(t.Context(), "clone")
	if err != nil {
		t.Fatal(err)
	}
	other := f.otherWorktree(cl, "wt-clone")
	msg := f.usageMsg(f.cli(t.TempDir(), "land", "feature/x", "--source-worktree", f.wt, "--work-worktree", other, "--json"))
	if !strings.Contains(msg, f.wt) || !strings.Contains(msg, other) {
		t.Fatalf("%q", msg)
	}
}

func TestANonexistentWorktreePathIsAUsageErrorNamingIt(t *testing.T) {
	f := newFx(t)
	f.feature()
	bogus := filepath.Join(t.TempDir(), "no-such-worktree")
	msg := f.usageMsg(f.cli(t.TempDir(), "land", "feature/x", "--work-worktree", bogus, "--json"))
	if !strings.Contains(msg, bogus) || !strings.Contains(msg, "does not exist") {
		t.Fatalf("%q", msg)
	}
}

func TestAWorktreePathOutsideAnyRepoIsAUsageErrorNamingIt(t *testing.T) {
	f := newFx(t)
	f.feature()
	plain := t.TempDir()
	msg := f.usageMsg(f.cli(t.TempDir(), "land", "feature/x", "--work-worktree", plain, "--json"))
	if !strings.Contains(msg, plain) || !strings.Contains(msg, "not inside a git repository") {
		t.Fatalf("%q", msg)
	}
}

func TestACwdInAnotherCloneThanTheFlagsIsRefusedNotFollowed(t *testing.T) {
	f := newFx(t)
	f.feature()
	sib, err := f.repo.NewSibling(t.Context(), "beta", gitfixture.RepoOptions{InitialCommit: true})
	if err != nil {
		t.Fatal(err)
	}
	msg := f.usageMsg(f.cli(sib.Dir, "land", "feature/x", "--source-worktree", f.wt, "--json"))
	if !strings.Contains(msg, sib.Dir) || !strings.Contains(msg, f.wt) {
		t.Fatalf("%q", msg)
	}
}

func TestACwdInAnotherWorktreeOfTheSameCloneIsAccepted(t *testing.T) {
	f := newFx(t)
	f.feature()
	r := f.cli(f.repo.Dir, "land", "feature/x", "--source-worktree", f.wt, "--json")
	if m := f.jsonOutcome(r); r.code != 0 || m["outcome"] != "landed" {
		t.Fatalf("%+v", r)
	}
}

func TestNoFlagsAndANonGitCwdKeepTheNotInsideARepositoryError(t *testing.T) {
	f := newFx(t)
	dir := t.TempDir()
	msg := f.usageMsg(f.cli(dir, "land", "b", "--json"))
	if !strings.Contains(msg, dir+" is not inside a git repository") {
		t.Fatalf("%q", msg)
	}
}

func TestResumeFlagsWithoutRepoOutsideARepoSayToPassRepo(t *testing.T) {
	f := newFx(t)
	for _, fl := range []string{"--continue", "--skip", "--abort", "--quit"} {
		msg := f.usageMsg(f.cli(t.TempDir(), "land", fl, "--json"))
		if !strings.Contains(msg, "--repo") || !strings.Contains(msg, "no worktree flags") {
			t.Fatalf("%s: %q", fl, msg)
		}
	}
}
