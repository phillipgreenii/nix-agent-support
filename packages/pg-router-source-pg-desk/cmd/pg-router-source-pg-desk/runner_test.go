// runner_test.go: the one real-process test (V-3 regression guard for the real
// exec path through the seam; everything else answers the Runner seam in
// process). It runs a #!/bin/sh stub, not the test binary. The behaviour of
// cmdrun.Exec itself (exit codes, separate stdout and stderr, env, stdin,
// signal death, context end, start errors) is covered by the shared
// cmdruntest.RunContract suite in x.
package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The seam end to end with the real runner: a real child that exits 2 with a
// body on stdout is classified like pg-desk's partial result.
func TestRunPgDeskThroughExecUsesTheRealPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/pg-desk", []byte("#!/bin/sh\nprintf '%s' \"$*\"\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	res, err := runPgDesk(context.Background(), []string{"widget", "changes"})
	if err != nil {
		t.Fatal(err)
	}
	if res.exitCode != 2 || !classifyExit(res.exitCode) || !strings.Contains(string(res.stdout), "widget changes") {
		t.Errorf("exit=%d stdout=%q", res.exitCode, res.stdout)
	}
}

// A pg-desk that is not on $PATH is a start error, not an exit code.
func TestRunPgDeskMissingBinaryIsAStartError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := runPgDesk(context.Background(), []string{"changes"}); err == nil {
		t.Fatal("want a start error when pg-desk is not on PATH")
	}
}
