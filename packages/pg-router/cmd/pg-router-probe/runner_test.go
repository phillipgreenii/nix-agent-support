// runner_test.go: the real exec path through the seam (V-3 regression guard
// behind the in-process doubles of testmain_test.go). Each test starts one
// #!/bin/sh stub (or a missing binary), never the race-built test binary, so
// none pays the race runtime's 1 s exit sleep. The behaviour of cmdrun.Exec
// itself (exit codes, separate stdout and stderr, env, stdin, signal death,
// context end, start errors) is covered by the shared cmdruntest.RunContract
// suite in x.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
)

// stubOnPath installs an executable shell script named name first on $PATH.
func stubOnPath(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A nil runner runs the real child: the argv crosses the seam and a nonzero
// exit is a failure that forwards the child's own stderr.
func TestCallConnectorThroughExecUsesTheRealPath(t *testing.T) {
	stubOnPath(t, client.Binary, `printf '%s' "$*"; printf err >&2; exit 2`)
	warn, msgs := warnCollector()
	_, err := callConnector(context.Background(), []string{"issue", "list"}, true, warn)
	if !errors.Is(err, errConnectorFailed) {
		t.Fatalf("callConnector: %v, want errConnectorFailed", err)
	}
	if len(*msgs) != 1 || (*msgs)[0] != "err" {
		t.Errorf("warnings = %v, want the child's stderr %q", *msgs, "err")
	}
}

// A pg-connector that is not on $PATH is a start error, not an exit code.
func TestCallConnectorMissingBinaryIsAStartError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	warn, msgs := warnCollector()
	if _, err := callConnector(context.Background(), []string{"issue", "list"}, true, warn); !errors.Is(err, errConnectorFailed) || len(*msgs) != 1 {
		t.Fatalf("got %v, warnings %v; want errConnectorFailed with one warning", err, *msgs)
	}
}

// `pg-router status --json` through the real runner, end to end.
func TestFetchBacklogFromStatusThroughExecUsesTheRealPath(t *testing.T) {
	stubOnPath(t, "pg-router", `printf '{"queues":[{"depth":3},{"depth":4}]}'`)
	opts := statusOpts(t)
	opts.pgRouterPath = "pg-router"
	got, err := fetchBacklogFromStatus(context.Background(), opts)
	if err != nil || got != 7 {
		t.Fatalf("got %d, %v; want 7, nil", got, err)
	}
}

// A real status child that outlives the probe's deadline is killed and
// classified as a timeout (retryable), not a plain failure.
func TestFetchBacklogFromStatusRealChildKilledByDeadlineIsATimeout(t *testing.T) {
	stubOnPath(t, "pg-router", `exec sleep 30`)
	opts := statusOpts(t)
	opts.pgRouterPath = "pg-router"
	opts.statusTimeout = 100 * time.Millisecond
	start := time.Now()
	_, err := fetchBacklogFromStatus(context.Background(), opts)
	if err == nil || !isTimeout(err) || !strings.Contains(err.Error(), "pg-router status --json") {
		t.Fatalf("got %v, want a timeout-classified error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("deadline not honored: took %v", elapsed)
	}
}
