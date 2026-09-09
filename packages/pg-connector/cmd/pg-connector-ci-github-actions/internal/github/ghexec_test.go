package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ghStubOnPath puts an executable named `gh` on PATH that records its own
// execution in the returned marker path. Callers assert the marker never
// appears: a gh that WOULD have recorded itself was never run.
func ghStubOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	script := "#!/bin/sh\n: > \"" + marker + "\"\nexit 9\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	// Prepended, so the stub shadows any real gh on the developer's PATH
	// while leaving other tools (git) reachable.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
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

// writeGHStub puts an executable named `gh` on PATH running the given
// POSIX shell script body, so Run/RunStdin can be exercised against a real
// child process (stdout, stderr, exit code) without a live gh install.
func writeGHStub(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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

// leakedGitDirFamily is the enumerated set from bead pg2-5xn2j's acceptance
// criteria: none of these may reach a gh child. gh resolves "which
// repository am I talking about" by shelling out to git in its working
// directory (`git remote -v` / `git rev-parse`), and those git children
// inherit gh's environment exactly as they would a direct git call — see
// internal/gitenv for the full mechanism.
var leakedGitDirFamily = []string{
	"GIT_DIR=/leaked/.git",
	"GIT_WORK_TREE=/leaked",
	"GIT_INDEX_FILE=/leaked/.git/index",
	"GIT_COMMON_DIR=/leaked/.git",
	"GIT_OBJECT_DIRECTORY=/leaked/.git/objects",
	"GIT_PREFIX=packages/pg-connector/",
	"GIT_CEILING_DIRECTORIES=/",
}

// enterpriseAndTargetVars is bead pg2-y23d4 #21's acceptance criteria: none
// of these may reach a gh child. Under an enterprise GH_HOST, gh prefers
// GH_ENTERPRISE_TOKEN/GITHUB_ENTERPRISE_TOKEN over the resolved GH_TOKEN
// this gateway injects, so an ambient enterprise credential would otherwise
// silently win; GH_REPO would override the explicit --repo this backend
// always passes.
var enterpriseAndTargetVars = []string{
	"GH_ENTERPRISE_TOKEN=ent-secret",
	"GITHUB_ENTERPRISE_TOKEN=ent-other",
	"GH_HOST=github.example.com",
	"GH_REPO=leaked/repo",
	"GH_CONFIG_DIR=/leaked/gh-config",
}

// TestCLICommand_SetsWaitDelay is the regression test for bead pg2-332z8
// #13's per-Cmd half: command() (this module's own choke point for every
// gh invocation) must set WaitDelay, independent of whatever deadline ctx
// itself carries — WaitDelay bounds Cmd.Wait's own residual wait for the
// stdout/stderr pipes to close (e.g. a grandchild inheriting one and
// holding it open), which a context deadline alone does not cover.
func TestCLICommand_SetsWaitDelay(t *testing.T) {
	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "resolved-tok"})
	cmd, err := cli.Command(context.Background(), "run", "list")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.WaitDelay != scriptout.DefaultWaitDelay {
		t.Fatalf("cmd.WaitDelay = %v, want %v", cmd.WaitDelay, scriptout.DefaultWaitDelay)
	}
}

// TestCLIRun_ErrorMessage_CapsStderr is the regression test for bead
// pg2-332z8 #26: before TruncateForFold, RunStdin folded gh's ENTIRE
// captured stderr into the returned error with no bound — a runaway or
// unexpectedly verbose gh failure could produce an unbounded error string.
func TestCLIRun_ErrorMessage_CapsStderr(t *testing.T) {
	huge := strings.Repeat("e", scriptout.MaxFoldedOutputBytes*3)
	ghStubExitingWithStderr(t, 1, huge)

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "resolved-tok"})
	_, err := cli.Run(context.Background(), "run", "list")
	if err == nil {
		t.Fatal("expected error from the failing gh stub")
	}
	if got := len(err.Error()); got > scriptout.MaxFoldedOutputBytes+256 {
		t.Fatalf("error message is %d bytes; stderr fold was not capped", got)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation marker in the error, got a %d-byte message", len(err.Error()))
	}
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

// countTokenEntries returns how many GH_TOKEN / GITHUB_TOKEN entries env
// holds and the value of the last GH_TOKEN seen.
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

// TestCLICommand_ExcludesEnterpriseAndTargetVars is the CLI.Command half of
// bead pg2-y23d4 #21's regression: this is the choke point every real
// `gh <args>` invocation in this module goes through (the token resolver's
// own exec is covered separately by
// TestGHAuthTokenCommand_ExcludesEnterpriseAndTargetVars in token_test.go).
func TestCLICommand_ExcludesEnterpriseAndTargetVars(t *testing.T) {
	for _, kv := range enterpriseAndTargetVars {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "resolved-tok"})
	cmd, err := cli.Command(context.Background(), "run", "list")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	for _, kv := range enterpriseAndTargetVars {
		k, _, _ := strings.Cut(kv, "=")
		for _, envKV := range cmd.Env {
			if strings.HasPrefix(envKV, k+"=") {
				t.Errorf("cmd.Env carries leaked %q into the gh child", k)
			}
		}
	}
}

// TestCLICommand_ExcludesLeakedGitDirFamily is the regression test for bead
// pg2-5xn2j, ported to this backend: cliGHRunner.command (defined in this
// file) is the choke point every gh invocation in this package goes
// through, so proving it here covers every caller.
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

// TestCLICommand_NoToken_RefusesBeforeExec is the core guarantee of bead
// pg2-ilzq9, ported to this backend: with no resolvable token the gateway
// hands back NO command at all, so gh cannot have been executed, and the
// error wraps this copy's local ErrGHAuthInvalid sentinel (not
// vcs.ErrAuthInvalid — this module has no pg-pr dependency) plus the action
// the user must take.
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
	if !errors.Is(err, ErrGHAuthInvalid) {
		t.Errorf("errors.Is(err, ErrGHAuthInvalid) = false, want true (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}
	assertGHNotExecuted(t, marker)
}

// TestCLIRun_NoToken_RefusesBeforeExec covers the Run/RunStdin face of the
// same gateway.
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
	if !errors.Is(err, ErrGHAuthInvalid) {
		t.Errorf("errors.Is(err, ErrGHAuthInvalid) = false, want true (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}
	assertGHNotExecuted(t, marker)
}

// TestCLICommand_InjectsExactlyOneGHToken asserts the child env the gateway
// builds: exactly one GH_TOKEN carrying the RESOLVED token, and no ambient
// GH_TOKEN/GITHUB_TOKEN surviving.
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

// tokenResult is one canned (token, error) outcome for countingTokenSource.
type tokenResult struct {
	tok string
	err error
}

// countingTokenSource returns its results in order as it is called,
// sticking on the last entry once exhausted, and counts how many times
// Token was invoked. It exists to prove cliGHRunner.token's caching
// behavior (ghexec.go) directly: this file merges what the pr-github
// sibling copy splits across github.go (the sync.Mutex-backed cache) and
// ghexec.go (the choke point) into one file, and that caching logic has no
// direct unit test on the sibling side (there it is only exercised
// indirectly through Provider-level tests) — so it needs its own coverage
// here.
type countingTokenSource struct {
	results []tokenResult
	calls   int
}

func (c *countingTokenSource) Token(context.Context) (string, error) {
	i := c.calls
	if i >= len(c.results) {
		i = len(c.results) - 1
	}
	c.calls++
	r := c.results[i]
	return r.tok, r.err
}

// TestCliGHRunner_CachesSuccessfulTokenAcrossCommands proves cliGHRunner.token
// resolves a successful token at most once: a second Command call on the
// same CLI must reuse the cached value rather than consulting the
// TokenSource again.
func TestCliGHRunner_CachesSuccessfulTokenAcrossCommands(t *testing.T) {
	src := &countingTokenSource{results: []tokenResult{{tok: "tok-1"}}}
	cli := NewCLIWithTokenSource(src)

	cmd1, err := cli.Command(context.Background(), "pr", "view")
	if err != nil {
		t.Fatalf("first Command: %v", err)
	}
	cmd2, err := cli.Command(context.Background(), "issue", "list")
	if err != nil {
		t.Fatalf("second Command: %v", err)
	}

	if src.calls != 1 {
		t.Errorf("TokenSource.Token called %d times, want exactly 1 (cached after first success)", src.calls)
	}
	for _, cmd := range []*exec.Cmd{cmd1, cmd2} {
		_, _, ghValue := countTokenEntries(cmd.Env)
		if ghValue != "tok-1" {
			t.Errorf("GH_TOKEN = %q, want tok-1", ghValue)
		}
	}
}

// TestCliGHRunner_DoesNotCacheFailedResolution proves the inverse: a failed
// resolution must NOT be cached, so a subsequent Command call retries the
// TokenSource and can succeed once the transient failure clears.
func TestCliGHRunner_DoesNotCacheFailedResolution(t *testing.T) {
	src := &countingTokenSource{results: []tokenResult{
		{err: errors.New("transient keychain error")},
		{tok: "tok-2"},
	}}
	cli := NewCLIWithTokenSource(src)

	if _, err := cli.Command(context.Background(), "pr", "view"); err == nil {
		t.Fatal("first Command: want error from failed token resolution")
	}
	cmd, err := cli.Command(context.Background(), "pr", "view")
	if err != nil {
		t.Fatalf("second Command (after retry): %v", err)
	}
	if src.calls != 2 {
		t.Errorf("TokenSource.Token called %d times, want exactly 2 (a failure must not be cached)", src.calls)
	}
	_, _, ghValue := countTokenEntries(cmd.Env)
	if ghValue != "tok-2" {
		t.Errorf("GH_TOKEN = %q, want tok-2", ghValue)
	}
}

// TestCLIRun_Success exercises Run against a real (stubbed) gh child
// process end to end: stdout is captured and returned, no error on a clean
// exit.
func TestCLIRun_Success(t *testing.T) {
	writeGHStub(t, "printf 'hello-stdout'\nexit 0\n")

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "tok"})
	out, err := cli.Run(context.Background(), "pr", "view")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "hello-stdout" {
		t.Errorf("Run stdout = %q, want %q", out, "hello-stdout")
	}
}

// TestCLIRunStdin_FeedsStdinToChild proves RunStdin actually wires its
// stdin argument to the child process: the stub echoes stdin back on
// stdout via `cat`, so the returned bytes must equal what was fed in.
func TestCLIRunStdin_FeedsStdinToChild(t *testing.T) {
	writeGHStub(t, "cat\nexit 0\n")

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "tok"})
	out, err := cli.RunStdin(context.Background(), []byte("payload-body"), "api", "graphql", "--input", "-")
	if err != nil {
		t.Fatalf("RunStdin: %v", err)
	}
	if string(out) != "payload-body" {
		t.Errorf("RunStdin returned stdout %q, want the fed stdin %q", out, "payload-body")
	}
}

// TestCLIRun_AuthFailureClassification proves RunStdin's failure path
// (ghexec.go) classifies an exit-4-with-login-hint gh failure as an auth
// failure and wraps ErrGHAuthInvalid, mirroring the exact IsAuthFailure
// case this backend's auth.go documents.
func TestCLIRun_AuthFailureClassification(t *testing.T) {
	writeGHStub(t, "echo 'gh: To get started with GitHub CLI, please run:  gh auth login' 1>&2\nexit 4\n")

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "tok"})
	_, err := cli.Run(context.Background(), "pr", "view")
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !errors.Is(err, ErrGHAuthInvalid) {
		t.Errorf("errors.Is(err, ErrGHAuthInvalid) = false, want true (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error does not name `gh auth login`: %v", err)
	}
}

// TestCLIRun_NonAuthFailure_NotClassifiedAsAuth proves the negative: a
// plain exit-1 failure with unrelated stderr must NOT be classified as an
// auth failure, and the error must still carry gh's stderr for
// diagnosability.
func TestCLIRun_NonAuthFailure_NotClassifiedAsAuth(t *testing.T) {
	writeGHStub(t, "echo 'some transient gh error' 1>&2\nexit 1\n")

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "tok"})
	_, err := cli.Run(context.Background(), "pr", "view")
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if errors.Is(err, ErrGHAuthInvalid) {
		t.Errorf("errors.Is(err, ErrGHAuthInvalid) = true, want false for a non-auth exit: %v", err)
	}
	if !strings.Contains(err.Error(), "some transient gh error") {
		t.Errorf("error does not carry gh's stderr: %v", err)
	}
}

// TestCLIRun_ExecNotFound proves the final RunStdin branch: when gh itself
// cannot be found/started (as opposed to running and failing), the error
// diagnoses a missing binary rather than being misread as a gh-side
// failure.
func TestCLIRun_ExecNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no gh anywhere on PATH

	cli := NewCLIWithTokenSource(&fakeTokenSource{tok: "tok"})
	_, err := cli.Run(context.Background(), "pr", "view")
	if err == nil {
		t.Fatal("Run: want error when gh is not on PATH")
	}
	if !strings.Contains(err.Error(), "is gh on PATH?") {
		t.Errorf("error does not diagnose a missing gh binary: %v", err)
	}
}
