package branch

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
)

// ---------------------------------------------------------------------------
// gh-auth harness (bead pg2-ilzq9)
//
// These tests drive the REAL token-protected gateway (github.CLI) behind a spy
// that records every command it manages to BUILD. That is the seam that matters:
// when token resolution fails the gateway returns no command at all, so there is
// nothing to execute — and a stub `gh` on PATH proves no process ran.
// ---------------------------------------------------------------------------

// ghFailTokenSource never resolves a token (missing/expired gh credential).
type ghFailTokenSource struct{}

func (ghFailTokenSource) Token(context.Context) (string, error) {
	return "", errors.New("no gh credential in test env")
}

// ghFixedTokenSource resolves a known token.
type ghFixedTokenSource struct{ tok string }

func (f ghFixedTokenSource) Token(context.Context) (string, error) { return f.tok, nil }

// spyCommander wraps the real gateway, recording the args and child env of each
// command actually built, and repointing it at a path that cannot exist so no
// real gh runs during the test.
type spyCommander struct {
	inner *github.CLI
	built [][]string
	envs  [][]string
}

func (s *spyCommander) Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	cmd, err := s.inner.Command(ctx, args...)
	if err != nil {
		return nil, err
	}
	s.built = append(s.built, append([]string(nil), args...))
	s.envs = append(s.envs, append([]string(nil), cmd.Env...))
	cmd.Path = filepath.Join(os.TempDir(), "pg-pr-no-such-gh")
	return cmd, nil
}

// ghStubOnPath puts an executable `gh` on PATH that records its own execution.
// Tests assert the marker never appears: a gh that WOULD have recorded itself
// was never run. It is never expected to execute, so it needs no working
// interpreter — a regression that does exec it fails the assertion either way.
// The stub dir is PREPENDED so git stays reachable for the repo fixtures.
func ghStubOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	script := "#!/bin/sh\n: > \"" + marker + "\"\nexit 9\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func assertGHNotExecuted(t *testing.T, marker string) {
	t.Helper()
	_, err := os.Stat(marker)
	switch {
	case err == nil:
		t.Fatal("gh WAS executed despite unresolvable auth (stub marker present)")
	case !errors.Is(err, fs.ErrNotExist):
		t.Fatalf("stat gh stub marker: %v", err)
	}
}

// assertSingleGHToken mirrors github's TestEnvWithGHToken assertions on the env
// a site's gh child would receive.
func assertSingleGHToken(t *testing.T, env []string, want string) {
	t.Helper()
	var ghCount, githubCount int
	var ghValue string
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GH_TOKEN="):
			ghCount++
			ghValue = strings.TrimPrefix(kv, "GH_TOKEN=")
		case strings.HasPrefix(kv, "GITHUB_TOKEN="):
			githubCount++
		}
	}
	if ghCount != 1 {
		t.Errorf("child env has %d GH_TOKEN entries, want exactly 1", ghCount)
	}
	if githubCount != 0 {
		t.Errorf("child env has %d GITHUB_TOKEN entries, want 0 (ambient must not leak)", githubCount)
	}
	if ghValue != want {
		t.Errorf("GH_TOKEN = %q, want %q (the resolved token)", ghValue, want)
	}
}

// ---------------------------------------------------------------------------
// site: internal/branch — CLIGHClient.PRForBranch
// ---------------------------------------------------------------------------

// TestPRForBranch_NoToken_DoesNotExecGH proves `pg-pr branch detect`'s PR lookup
// cannot reach an unauthenticated gh.
func TestPRForBranch_NoToken_DoesNotExecGH(t *testing.T) {
	marker := ghStubOnPath(t)
	spy := &spyCommander{inner: github.NewCLIWithTokenSource(ghFailTokenSource{})}
	c := &CLIGHClient{gh: spy}

	pr, err := c.PRForBranch(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("PRForBranch = nil error with no resolvable token")
	}
	if pr != nil {
		t.Errorf("PRForBranch returned PR %d, want nil", *pr)
	}
	if !errors.Is(err, vcs.ErrAuthInvalid) {
		t.Errorf("errors.Is(err, vcs.ErrAuthInvalid) = false, want true (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}
	if len(spy.built) != 0 {
		t.Errorf("gh command was built despite failed token resolution: %v", spy.built)
	}
	assertGHNotExecuted(t, marker)
}

// TestDetect_NoToken_StillReportsGitFacts pins the documented contract that PR
// lookup is best-effort: an unresolvable gh credential leaves pr_id null instead
// of failing the subcommand — and, per the refusal above, without ever reaching
// an interactive gh.
func TestDetect_NoToken_StillReportsGitFacts(t *testing.T) {
	marker := ghStubOnPath(t)
	dir := filepath.Join(t.TempDir(), "repo")
	initRepo(t, dir)
	addRemote(t, dir, "git@github.com:owner/repo.git")
	spy := &spyCommander{inner: github.NewCLIWithTokenSource(ghFailTokenSource{})}

	info, err := Detect(context.Background(), dir, Options{GH: &CLIGHClient{gh: spy}})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.PRNumber != nil {
		t.Errorf("PRNumber = %d, want nil", *info.PRNumber)
	}
	if info.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want owner/repo", info.Repo)
	}
	assertGHNotExecuted(t, marker)
}

// TestPRForBranch_WithToken_InjectsSingleGHToken asserts the child env of the gh
// this site invokes once a token IS resolved.
func TestPRForBranch_WithToken_InjectsSingleGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-gh")
	t.Setenv("GITHUB_TOKEN", "ambient-github")
	spy := &spyCommander{inner: github.NewCLIWithTokenSource(ghFixedTokenSource{tok: "resolved-tok"})}
	c := &CLIGHClient{gh: spy}

	// The built command is neutralized, so this fails on exec — the point is the
	// env it was handed, and that the failure is NOT an auth refusal.
	if _, err := c.PRForBranch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected the neutralized command to fail")
	} else if errors.Is(err, vcs.ErrAuthInvalid) {
		t.Errorf("resolved token still produced an auth error: %v", err)
	}
	if len(spy.built) != 1 {
		t.Fatalf("built %d gh commands, want 1: %v", len(spy.built), spy.built)
	}
	assertSingleGHToken(t, spy.envs[0], "resolved-tok")
}
