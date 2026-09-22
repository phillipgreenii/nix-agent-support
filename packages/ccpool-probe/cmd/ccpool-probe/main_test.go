package main

import (
	"bytes"
	"testing"
)

func TestRunUsageErrorOnUnknownFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"run", "--not-a-real-flag"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 (usage error), got %d (stderr=%q)", code, errOut.String())
	}
}

func TestRunUsageErrorOnUnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"not-a-real-subcommand"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 (usage error), got %d (stderr=%q)", code, errOut.String())
	}
}

// TestRunTotalFailureExitCodeThroughCLI exercises the total-failure exit
// code end to end through cobra's own flag parsing (unlike run_test.go's
// runProbe-level tests). Neither ccpoolExecCmdFactory nor execCmdFactory
// is overridden here, so "run" uses the REAL exec.CommandContext -- an
// empty PATH deterministically fails both the ccpool and pg-connector
// subprocess launches (exec: "ccpool"/"pg-connector": executable file not
// found in $PATH) without depending on either binary's real behavior.
func TestRunTotalFailureExitCodeThroughCLI(t *testing.T) {
	t.Setenv("PATH", "")
	var out, errOut bytes.Buffer
	code := run([]string{"run", "--snapshot-path", t.TempDir() + "/snapshot.json"}, &out, &errOut)
	if code != 3 {
		t.Fatalf("expected exit 3 (total failure), got %d (stderr=%q)", code, errOut.String())
	}
}
