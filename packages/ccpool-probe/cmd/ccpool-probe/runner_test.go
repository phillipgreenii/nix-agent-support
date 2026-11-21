// runner_test.go: the real-process tests (V-3 regression guard for the real
// exec path through both seams; everything else answers the Runner seams in
// process). Each test runs a #!/bin/sh stub found on a temporary $PATH, never
// the race-built test binary, so none pays the race runtime's 1 s exit sleep.
// The behaviour of cmdrun.Exec itself (exit codes, separate stdout and
// stderr, env, stdin, signal death, context end, start errors) is covered by
// the shared cmdruntest.RunContract suite in x.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
)

// stubOnPath installs an executable #!/bin/sh stub named name running body,
// and makes it (plus the system dirs the stub needs) the whole $PATH.
func stubOnPath(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

// The ccpool seam end to end with the real runner (nil ccpoolRunner): argv,
// the CCPOOL_POOL override appended to the inherited environment, stdout,
// stderr and a non-zero exit all cross the seam.
func TestRunCcpoolThroughExecUsesTheRealPath(t *testing.T) {
	t.Setenv("CCPOOL_PROBE_RUNNER_AMBIENT", "ambient")
	stubOnPath(t, "ccpool", `printf '%s|%s|%s' "$CCPOOL_PROBE_RUNNER_AMBIENT" "$CCPOOL_POOL" "$*"; echo err >&2; exit 3`)
	res, err := runCcpool(context.Background(), "/pool/x", []string{"list", "--all"})
	if err != nil {
		t.Fatal(err)
	}
	if res.exitCode != 3 || string(res.stdout) != "ambient|/pool/x|list --all" || string(res.stderr) != "err\n" {
		t.Errorf("exit=%d stdout=%q stderr=%q", res.exitCode, res.stdout, res.stderr)
	}
}

// The pg-connector seam end to end with the real runner (nil connectorRunner):
// the argv crosses the seam and a nonzero exit forwards the child's own stderr.
func TestCallConnectorThroughExecUsesTheRealPath(t *testing.T) {
	stubOnPath(t, client.Binary, `printf '%s' "$*"; echo err >&2; exit 4`)
	var msgs []string
	_, err := callConnector(context.Background(), []string{"issue", "list"}, true, func(m string) { msgs = append(msgs, m) })
	if !errors.Is(err, errConnectorFailed) {
		t.Fatalf("callConnector: %v, want errConnectorFailed", err)
	}
	if len(msgs) != 1 || msgs[0] != "err\n" {
		t.Errorf("warnings = %q, want the child's stderr %q", msgs, "err\n")
	}
}

// A subprocess that is not on $PATH is a start error, not an exit code.
func TestMissingBinaryIsAStartErrorOnBothSeams(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := runCcpool(context.Background(), "", []string{"list"}); err == nil || !strings.Contains(err.Error(), "ccpool: exec") {
		t.Errorf("runCcpool: want a start error, got %v", err)
	}
	var msgs []string
	if _, err := callConnector(context.Background(), []string{"issue", "list"}, true, func(m string) { msgs = append(msgs, m) }); !errors.Is(err, errConnectorFailed) || len(msgs) != 1 || !strings.Contains(msgs[0], "exit -1") {
		t.Errorf("callConnector: want a start-error warning and errConnectorFailed, got %v, %q", err, msgs)
	}
}
