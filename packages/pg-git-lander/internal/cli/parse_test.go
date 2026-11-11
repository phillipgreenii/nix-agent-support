package cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cli"
)

func mustParse(t *testing.T, args ...string) *cli.Command {
	t.Helper()
	c, err := cli.Parse(args)
	if err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}
	return c
}

func wantUsage(t *testing.T, contains string, args ...string) {
	t.Helper()
	c, err := cli.Parse(args)
	if err == nil {
		t.Fatalf("Parse(%v) = %+v, want a usage error", args, c)
	}
	if !strings.Contains(err.Msg, contains) {
		t.Fatalf("Parse(%v) error %q does not contain %q", args, err.Msg, contains)
	}
}

func TestParseRebaseFullForm(t *testing.T) {
	c := mustParse(t, "rebase", "feature/x", "--onto", "main", "--source-worktree", "/s", "--work-worktree=/w",
		"--wait", "90s", "--context", "a=b", "--context", "c=d=e", "--json", "--repo", "/r")
	if c.Kind != cli.KindRebase || c.Branch != "feature/x" || c.Onto != "main" || c.SourceWorktree != "/s" ||
		c.WorkWorktree != "/w" || !c.JSON || c.Repo != "/r" || c.Resume != "" {
		t.Fatalf("%+v", c)
	}
	if c.Wait == nil || *c.Wait != 90*time.Second {
		t.Fatalf("wait = %v", c.Wait)
	}
	if len(c.Context) != 2 || c.Context[1].Key != "c" || c.Context[1].Value != "d=e" {
		t.Fatalf("context = %+v", c.Context)
	}
}

func TestParseLandFullForm(t *testing.T) {
	c := mustParse(t, "land", "--into", "develop", "feature/x", "--target-worktree", "/t", "--source-worktree", "/s",
		"--work-worktree", "/w", "--keep-source-worktree", "--keep-source-branch", "--wait", "1m", "--json")
	if c.Kind != cli.KindLand || c.Branch != "feature/x" || c.Into != "develop" || c.TargetWorktree != "/t" ||
		!c.KeepSourceWorktree || !c.KeepSourceBranch || *c.Wait != time.Minute {
		t.Fatalf("%+v", c)
	}
}

func TestParseLandDefaultsTargetToEmpty(t *testing.T) {
	if c := mustParse(t, "land", "b"); c.Into != "" {
		t.Fatalf("into = %q", c.Into)
	}
}

func TestParseResumeForms(t *testing.T) {
	for _, kind := range []string{"rebase", "land"} {
		for _, flag := range []string{"--continue", "--skip", "--abort", "--quit"} {
			c := mustParse(t, kind, "b", flag, "--wait", "5s", "--json", "--repo", "/r")
			if c.Resume != flag || c.Branch != "b" || c.Repo != "/r" {
				t.Fatalf("%s %s: %+v", kind, flag, c)
			}
			c = mustParse(t, kind, flag)
			if c.Resume != flag || c.Branch != "" {
				t.Fatalf("%s %s without BRANCH: %+v", kind, flag, c)
			}
		}
	}
}

func TestParseStatusLogConfigCheck(t *testing.T) {
	if c := mustParse(t, "status", "--json", "--repo", "/r"); c.Kind != cli.KindStatus || c.Branch != "" || !c.JSON {
		t.Fatalf("%+v", c)
	}
	if c := mustParse(t, "status", "b"); c.Branch != "b" {
		t.Fatalf("%+v", c)
	}
	if c := mustParse(t, "log", "op-1", "--tail", "20", "--repo", "/r"); c.Kind != cli.KindLog || c.Branch != "op-1" || c.Tail != 20 {
		t.Fatalf("%+v", c)
	}
	if c := mustParse(t, "config", "check", "--file", "/f", "--json", "--repo", "/r"); c.Kind != cli.KindConfigCheck || c.File != "/f" || !c.JSON || c.Repo != "/r" {
		t.Fatalf("%+v", c)
	}
	if c := mustParse(t, "config", "check"); c.File != "" {
		t.Fatalf("%+v", c)
	}
}

func TestEveryFormAcceptsRepo(t *testing.T) {
	for _, args := range [][]string{
		{"rebase", "b", "--onto", "main"},
		{"land", "b"},
		{"rebase", "--abort"},
		{"land", "b", "--quit"},
		{"status"},
		{"log", "b"},
		{"config", "check"},
	} {
		c := mustParse(t, append(args, "--repo", "/somewhere")...)
		if c.Repo != "/somewhere" {
			t.Errorf("%v: repo = %q", args, c.Repo)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	wantUsage(t, "subcommand is required")
	wantUsage(t, "unknown subcommand", "frobnicate")
	wantUsage(t, "needs BRANCH", "rebase", "--onto", "main")
	wantUsage(t, "needs BRANCH", "land")
	wantUsage(t, "--onto", "rebase", "b")
	wantUsage(t, "needs a value", "rebase", "b", "--onto")
	wantUsage(t, "--wait needs a duration", "land", "b", "--wait", "soon")
	wantUsage(t, "KEY=VALUE", "land", "b", "--context", "novalue")
	wantUsage(t, "unknown flag", "land", "b", "-x")
	wantUsage(t, "does not take the flag --onto", "land", "b", "--onto", "main")
	wantUsage(t, "does not take the flag --into", "rebase", "b", "--onto", "main", "--into", "x")
	wantUsage(t, "cannot be combined", "land", "b", "--continue", "--abort")
	wantUsage(t, "unexpected argument", "land", "a", "b")
	wantUsage(t, "log needs", "log")
	wantUsage(t, "config takes", "config")
}

func TestBranchEqualToTargetIsAUsageError(t *testing.T) {
	wantUsage(t, "BRANCH and TARGET are both feature/x", "land", "feature/x", "--into", "feature/x")
}

func TestKeepFlagsWithRebaseAreUsageErrors(t *testing.T) {
	wantUsage(t, "--keep-source-worktree", "rebase", "b", "--onto", "main", "--keep-source-worktree")
	wantUsage(t, "--keep-source-branch", "rebase", "b", "--onto", "main", "--keep-source-branch")
}

func TestResumeRejectsFreshOnlyFlags(t *testing.T) {
	for _, extra := range [][]string{
		{"--context", "k=v"},
		{"--source-worktree", "/s"},
		{"--work-worktree", "/w"},
		{"--target-worktree", "/t"},
		{"--keep-source-worktree"},
		{"--keep-source-branch"},
		{"--into", "main"},
	} {
		args := append([]string{"land", "b", "--continue"}, extra...)
		wantUsage(t, "not accepted with --continue", args...)
	}
	wantUsage(t, "not accepted with --abort", "rebase", "b", "--abort", "--onto", "main")
	wantUsage(t, "not accepted with --quit", "rebase", "--quit", "--source-worktree", "/s")
}

func TestUsageErrorKnowsJSON(t *testing.T) {
	if _, err := cli.Parse([]string{"land", "--json"}); err == nil || !err.JSON {
		t.Fatalf("%+v", err)
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestApplyEnvFillsAbsentFlagsOnly(t *testing.T) {
	env := envOf(map[string]string{
		cli.EnvTargetWorktree: "/et", cli.EnvSourceWorktree: "/es", cli.EnvWorkWorktree: "/ew",
	})
	c := mustParse(t, "land", "b", "--source-worktree", "/flag")
	c.ApplyEnv(env)
	if c.TargetWorktree != "/et" || c.SourceWorktree != "/flag" || c.WorkWorktree != "/ew" {
		t.Fatalf("%+v", c)
	}
}

func TestApplyEnvForRebaseSkipsTargetWorktree(t *testing.T) {
	c := mustParse(t, "rebase", "b", "--onto", "main")
	c.ApplyEnv(envOf(map[string]string{cli.EnvTargetWorktree: "/et", cli.EnvSourceWorktree: "/es"}))
	if c.TargetWorktree != "" || c.SourceWorktree != "/es" {
		t.Fatalf("%+v", c)
	}
}

func TestApplyEnvIsIgnoredOnResume(t *testing.T) {
	c := mustParse(t, "land", "b", "--continue")
	c.ApplyEnv(envOf(map[string]string{
		cli.EnvTargetWorktree: "/et", cli.EnvSourceWorktree: "/es", cli.EnvWorkWorktree: "/ew",
	}))
	if c.TargetWorktree != "" || c.SourceWorktree != "" || c.WorkWorktree != "" {
		t.Fatalf("a resume read the PGL_* variables: %+v", c)
	}
}

func TestEnvNamesAvoidTheHookPrefix(t *testing.T) {
	for _, n := range []string{cli.EnvTargetWorktree, cli.EnvSourceWorktree, cli.EnvWorkWorktree} {
		if strings.HasPrefix(n, "PG_GIT_LANDER_") || !strings.HasPrefix(n, "PGL_") {
			t.Errorf("%s", n)
		}
	}
}
