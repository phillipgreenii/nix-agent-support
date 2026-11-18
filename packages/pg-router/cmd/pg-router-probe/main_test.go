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

func TestRunTotalFailureExitCodeThroughCLI(t *testing.T) {
	var out, errOut bytes.Buffer
	// "run" with every sub-check unconfigured -- total failure (exit 3),
	// end to end through cobra's own flag parsing this time (unlike
	// run_test.go's runProbe-level tests).
	code := run([]string{"run", "--snapshot-path", t.TempDir() + "/snapshot.json"}, &out, &errOut)
	if code != 3 {
		t.Fatalf("expected exit 3 (total failure), got %d (stderr=%q)", code, errOut.String())
	}
}
