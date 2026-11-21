// exec_test.go: the one real-process check of this adapter's own wiring (the
// V-3 regression guard behind the in-process Fake of testmain_test.go). The
// real cmdrun.Exec is covered by x's cmdruntest.RunContract; what is this
// package's own is that, with connectorRunner unset, runPgConnector execs the
// ambient "pg-connector" binary with its argv and the --beads-dir env entry.
// It starts one /bin/sh script (never the race-built test binary), so it pays
// no race runtime exit sleep.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPgConnector_NilRunnerExecsAmbientBinaryWithArgsAndBeadsDirEnv(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s|%s' \"$*\" \"$PG_CONNECTOR_ISSUE_BEADS_DIR\"\necho oops >&2\nexit 2\n"
	if err := os.WriteFile(filepath.Join(dir, pgConnectorBinary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := connectorRunner
	connectorRunner = nil
	t.Cleanup(func() { connectorRunner = orig })

	res, err := runPgConnector(context.Background(), []string{"changes", "issue"}, beadsDirEnv("/real/beads"))
	if err != nil {
		t.Fatalf("runPgConnector: %v", err)
	}
	if got, want := string(res.stdout), "changes issue|/real/beads"; got != want {
		t.Fatalf("stdout = %q, want %q (argv and PG_CONNECTOR_ISSUE_BEADS_DIR must reach the real child)", got, want)
	}
	if res.exitCode != 2 || !strings.Contains(string(res.stderr), "oops") {
		t.Fatalf("got exit %d stderr %q, want exit 2 and the child's own stderr", res.exitCode, res.stderr)
	}
}

func TestRunPgConnector_MissingBinaryIsStartError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	orig := connectorRunner
	connectorRunner = nil
	t.Cleanup(func() { connectorRunner = orig })

	if _, err := runPgConnector(context.Background(), []string{"changes"}, nil); err == nil {
		t.Fatal("want a start error when pg-connector is not on PATH")
	}
}
