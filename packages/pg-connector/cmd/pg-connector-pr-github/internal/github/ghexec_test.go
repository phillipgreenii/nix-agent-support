package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/vcs"
)

// ghStubOnPath puts an executable named `gh` on PATH that records its own
// execution in the returned marker path. Callers assert the marker never
// appears: a gh that WOULD have recorded itself was never run.
//
// The stub is never expected to execute, so it needs no working interpreter at
// test time — a regression that does exec it fails the assertion whether or not
// /bin/sh is present in the build sandbox (either the marker appears, or the
// error is a plain exec failure instead of the auth error the test demands).
func ghStubOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	script := "#!/bin/sh\n: > \"" + marker + "\"\nexit 9\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	// Prepended, so the stub shadows any real gh on the developer's PATH while
	// leaving other tools (git) reachable.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// ghStubExitingWithStderr puts an executable named `gh` on PATH that writes
// stderrMsg to its standard error and exits with exitCode, so a real gh
// invocation surfaces exactly the message a test hands it — no other error
// text is manufactured.
func ghStubExitingWithStderr(t *testing.T, exitCode int, stderrMsg string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'GHSTUBEOF' >&2\n" + stderrMsg + "\nGHSTUBEOF\nexit " + fmt.Sprint(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// assertGHNotExecuted fails when the ghStubOnPath marker exists.
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

// leakedGitDirFamily is the enumerated set from bead pg2-5xn2j's acceptance
// criteria: none of these may reach a gh child. gh resolves "which repository
// am I talking about" by shelling out to git in its working directory (`git
// remote -v` / `git rev-parse`), and those git children inherit gh's
// environment exactly as they would a direct git call — see internal/gitenv
// for the full mechanism (proven on pg2-67h4y, fixed for direct git exec
// sites on pg2-lx41y).
var leakedGitDirFamily = []string{
	"GIT_DIR=/leaked/.git",
	"GIT_WORK_TREE=/leaked",
	"GIT_INDEX_FILE=/leaked/.git/index",
	"GIT_COMMON_DIR=/leaked/.git",
	"GIT_OBJECT_DIRECTORY=/leaked/.git/objects",
	"GIT_PREFIX=packages/pg-pr/",
	"GIT_CEILING_DIRECTORIES=/",
}

// envKeySet returns the set of variable names present in env.
func envKeySet(env []string) map[string]bool {
	keys := make(map[string]bool, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			keys[k] = true
		}
	}
	return keys
}

// assertNoLeakedGitDirFamily fails for every key in leakedGitDirFamily that
// is present in env.
func assertNoLeakedGitDirFamily(t *testing.T, env []string) {
	t.Helper()
	present := envKeySet(env)
	for _, kv := range leakedGitDirFamily {
		k, _, _ := strings.Cut(kv, "=")
		if present[k] {
			t.Errorf("cmd.Env carries leaked %q into the gh child", k)
		}
	}
}

// TestCLICommand_ExcludesLeakedGitDirFamily is the regression test for bead
// pg2-5xn2j: ghexec.go's command() is the choke point every gh invocation in
// this module (except the token resolver) goes through, so proving it here
// covers every caller — including internal/branch's PRForBranch and
// internal/worktree's PRExists, which both set cmd.Dir/--repo on the
// *exec.Cmd this method returns and would otherwise silently lose to an
// inherited GIT_DIR (git's repo discovery consults the environment before
// -C/--repo).
func TestCLICommand_ExcludesLeakedGitDirFamily(t *testing.T) {
	for _, kv := range leakedGitDirFamily {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "resolved-tok"})
	cmd, err := cli.Command(context.Background(), "pr", "view", "1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	assertNoLeakedGitDirFamily(t, cmd.Env)
}

// enterpriseAndTargetVars is bead pg2-y23d4 #21's acceptance criteria: none
// of these may reach a gh child either, alongside leakedGitDirFamily above.
// Under an enterprise GH_HOST, gh prefers GH_ENTERPRISE_TOKEN/
// GITHUB_ENTERPRISE_TOKEN over the resolved GH_TOKEN this gateway injects,
// so an ambient enterprise credential would otherwise silently win; GH_REPO
// would override an explicit --repo a caller passes.
var enterpriseAndTargetVars = []string{
	"GH_ENTERPRISE_TOKEN=ent-secret",
	"GITHUB_ENTERPRISE_TOKEN=ent-other",
	"GH_HOST=github.example.com",
	"GH_REPO=leaked/repo",
	"GH_CONFIG_DIR=/leaked/gh-config",
}

// assertNoEnterpriseOrTargetVars fails for every key in
// enterpriseAndTargetVars that is present in env.
func assertNoEnterpriseOrTargetVars(t *testing.T, env []string) {
	t.Helper()
	present := envKeySet(env)
	for _, kv := range enterpriseAndTargetVars {
		k, _, _ := strings.Cut(kv, "=")
		if present[k] {
			t.Errorf("cmd.Env carries leaked %q into the gh child", k)
		}
	}
}

// TestCLICommand_ExcludesEnterpriseAndTargetVars is the CLI.Command half of
// bead pg2-y23d4 #21's regression: this is the choke point every real
// `gh <args>` invocation in this module goes through (the token resolver's
// own exec is covered separately by TestGHAuthTokenCommand_ExcludesEnterpriseAndTargetVars
// in token_test.go), and under an enterprise GH_HOST an ambient
// GH_ENTERPRISE_TOKEN/GITHUB_ENTERPRISE_TOKEN would otherwise outrank the
// resolved token this gateway just injected.
func TestCLICommand_ExcludesEnterpriseAndTargetVars(t *testing.T) {
	for _, kv := range enterpriseAndTargetVars {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "resolved-tok"})
	cmd, err := cli.Command(context.Background(), "pr", "view", "1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	assertNoEnterpriseOrTargetVars(t, cmd.Env)
}

// countTokenEntries returns how many GH_TOKEN / GITHUB_TOKEN entries env holds
// and the value of the last GH_TOKEN seen.
func countTokenEntries(env []string) (ghCount, githubCount int, ghValue string) {
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GH_TOKEN="):
			ghCount++
			ghValue = strings.TrimPrefix(kv, "GH_TOKEN=")
		case strings.HasPrefix(kv, "GITHUB_TOKEN="):
			githubCount++
		}
	}
	return ghCount, githubCount, ghValue
}

// TestCLICommand_NoToken_RefusesBeforeExec is the core guarantee of bead
// pg2-ilzq9: with no resolvable token the gateway hands back NO command at all,
// so gh cannot have been executed, and the error carries the sentinel the
// daemon keys on plus the action the user must take.
func TestCLICommand_NoToken_RefusesBeforeExec(t *testing.T) {
	marker := ghStubOnPath(t)

	cli := NewCLIWithTokenSource(&fakeTokenSource{err: errors.New("keychain unavailable")})
	cmd, err := cli.Command(context.Background(), "pr", "view", "1")

	if cmd != nil {
		t.Fatalf("Command returned a non-nil *exec.Cmd (%v) despite failed token resolution", cmd.Args)
	}
	if err == nil {
		t.Fatal("Command returned nil error with no resolvable token")
	}
	if !errors.Is(err, vcs.ErrAuthInvalid) {
		t.Errorf("errors.Is(err, vcs.ErrAuthInvalid) = false, want true (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}
	assertGHNotExecuted(t, marker)
}

// TestCLIRun_NoToken_RefusesBeforeExec covers the Run/RunStdin face of the same
// gateway (the path the github VCS provider itself takes).
func TestCLIRun_NoToken_RefusesBeforeExec(t *testing.T) {
	marker := ghStubOnPath(t)

	cli := NewCLIWithTokenSource(&fakeTokenSource{err: errors.New("keychain unavailable")})
	out, err := cli.Run(context.Background(), "api", "graphql")

	if len(out) != 0 {
		t.Errorf("Run returned stdout %q, want empty", out)
	}
	if err == nil {
		t.Fatal("Run returned nil error with no resolvable token")
	}
	if !errors.Is(err, vcs.ErrAuthInvalid) {
		t.Errorf("errors.Is(err, vcs.ErrAuthInvalid) = false, want true (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}
	assertGHNotExecuted(t, marker)
}

// TestCLICommand_InjectsExactlyOneGHToken asserts the child env the gateway
// builds: exactly one GH_TOKEN carrying the RESOLVED token, and no ambient
// GH_TOKEN/GITHUB_TOKEN surviving. Same assertions as TestEnvWithGHToken, made
// on the command a caller actually gets.
func TestCLICommand_InjectsExactlyOneGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-gh")
	t.Setenv("GITHUB_TOKEN", "ambient-github")

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "resolved-tok"})
	cmd, err := cli.Command(context.Background(), "pr", "view", "1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	ghCount, githubCount, ghValue := countTokenEntries(cmd.Env)
	if ghCount != 1 {
		t.Errorf("child env has %d GH_TOKEN entries, want exactly 1", ghCount)
	}
	if githubCount != 0 {
		t.Errorf("child env has %d GITHUB_TOKEN entries, want 0 (ambient must not leak)", githubCount)
	}
	if ghValue != "resolved-tok" {
		t.Errorf("GH_TOKEN = %q, want the resolved token", ghValue)
	}
}

// TestGHCLITokenSource_CallableWithoutInjection guards against a later "fix"
// that routes the token RESOLVER through the protected gateway, which would be
// circular (the gateway needs the resolver to produce the token). Outcome the
// resolver must keep showing: it tries to exec gh itself, so with no gh on PATH
// it fails with an exec error — NOT with the gateway's auth-preflight refusal.
func TestGHCLITokenSource_CallableWithoutInjection(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no gh anywhere on PATH

	_, err := ghCLITokenSource{}.Token(context.Background())
	if err == nil {
		t.Fatal("Token() = nil error with no gh on PATH; resolver did not exec gh")
	}
	if errors.Is(err, vcs.ErrAuthInvalid) {
		t.Errorf("resolver returned the gateway's auth refusal (%v) — it must exec gh directly, not through the preflight", err)
	}
	if !strings.Contains(err.Error(), "gh auth token") {
		t.Errorf("error %v does not mention the `gh auth token` exec", err)
	}
}
