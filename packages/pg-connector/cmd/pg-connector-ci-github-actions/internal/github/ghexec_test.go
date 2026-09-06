package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeTokenSource is a TokenSource stub for chain/CLI tests; it records
// whether it was consulted so callers can assert short-circuiting.
type fakeTokenSource struct {
	tok    string
	err    error
	called bool
}

func (f *fakeTokenSource) Token(_ context.Context) (string, error) {
	f.called = true
	return f.tok, f.err
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
