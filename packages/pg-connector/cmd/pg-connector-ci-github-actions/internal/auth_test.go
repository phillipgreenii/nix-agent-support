// auth_test.go is carried over from
// packages/pg-pr/pkg/provider/cicd/ghactions/ghauth_test.go
// [contract: "Tests: carry over the existing test files alongside the
// ported implementation (adapted types)"], adapted to this backend's own
// internal/github package (no packages/pg-pr dependency, no local vcs
// package — see internal/github/auth.go's doc comment) and simplified: the
// original test wrapped a spyCommander around github.CLI.Command to
// capture the child env a *separate* cliGHRunner (ghactions.go's own type,
// not carried over here — see provider.go's ghRunner doc comment) would
// have built. Here Backend's ghRunner seam is satisfied directly by
// *internal/github.CLI, whose exported Command method is asserted on
// directly instead.
package internal

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ghFailTokenSource never resolves a token (missing/expired gh credential).
type ghFailTokenSource struct{}

func (ghFailTokenSource) Token(context.Context) (string, error) {
	return "", errors.New("no gh credential in test env")
}

// ghFixedTokenSource resolves a known token.
type ghFixedTokenSource struct{ tok string }

func (f ghFixedTokenSource) Token(context.Context) (string, error) { return f.tok, nil }

// ghStubOnPath puts an executable `gh` on PATH that records its own
// execution. Tests assert the marker never appears: a gh that WOULD have
// recorded itself was never run. Carried over unchanged from
// ghauth_test.go.
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

// assertSingleGHToken mirrors ghauth_test.go's own assertions on the env a
// site's gh child would receive.
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

// TestGHCLI_NoToken_DoesNotExecGH proves this backend cannot reach an
// unauthenticated gh through internal/github.CLI: no command is built (a
// nil *exec.Cmd, an error wrapping ErrGHAuthInvalid), and — via Backend —
// nothing is executed on PATH either.
func TestGHCLI_NoToken_DoesNotExecGH(t *testing.T) {
	marker := ghStubOnPath(t)
	cli := github.NewCLIWithTokenSource(ghFailTokenSource{})

	if _, err := cli.Command(context.Background(), "run", "list"); err == nil {
		t.Fatal("Command = nil error with no resolvable token")
	} else if !errors.Is(err, github.ErrGHAuthInvalid) {
		t.Errorf("errors.Is(err, github.ErrGHAuthInvalid) = false, want true (err=%v)", err)
	} else if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}

	p := NewWithDeps(cli, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if _, err := p.ListRuns(context.Background(), "foo/bar#42"); err == nil {
		t.Fatal("ListRuns = nil error with no resolvable token")
	} else if !errors.Is(err, scriptout.ErrUnauthenticated) {
		// classifyGHError (provider.go) translates the underlying
		// ErrGHAuthInvalid into scriptout's own closed taxonomy.
		t.Errorf("errors.Is(err, scriptout.ErrUnauthenticated) = false, want true (err=%v)", err)
	}
	assertGHNotExecuted(t, marker)
}

// TestGHCLI_WithToken_CommandInjectsSingleGHToken asserts the child env of
// the gh command internal/github.CLI.Command builds once a token IS
// resolved: exactly one GH_TOKEN, and no leaked ambient GITHUB_TOKEN.
func TestGHCLI_WithToken_CommandInjectsSingleGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-gh")
	t.Setenv("GITHUB_TOKEN", "ambient-github")
	cli := github.NewCLIWithTokenSource(ghFixedTokenSource{tok: "resolved-tok"})

	cmd, err := cli.Command(context.Background(), "run", "list")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	assertSingleGHToken(t, cmd.Env, "resolved-tok")
}
