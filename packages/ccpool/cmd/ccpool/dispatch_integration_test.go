//go:build integration

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// runHelpish runs the built binary-under-test with args, in the test's own
// (unmodified) environment. Safe for --help/no-args/unknown-subcommand paths
// only: none of them ever reach config.Load()/store.Open, so no XDG
// isolation is needed here (contrast with integration_test.go's runCC,
// which always sets up isolated XDG dirs for paths that DO touch the store).
func runHelpish(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// TestEndToEnd_TopLevelHelpListsEverySubcommand is the end-to-end regression
// test for pg2-htmkq's exact repro: `ccpool --help` used to print "Usage of
// list:" (list's own 5 flags), `ccpool help` used to run the full session
// table, and bare `ccpool` also defaulted to `list`. All four spellings must
// now print the same top-level usage, exit 0, and list every real
// subcommand.
func TestEndToEnd_TopLevelHelpListsEverySubcommand(t *testing.T) {
	bin := buildCCPool(t)
	for _, args := range [][]string{
		nil,
		{"--help"},
		{"-h"},
		{"help"},
	} {
		out, code := runHelpish(t, bin, args...)
		if code != 0 {
			t.Errorf("ccpool %v exit=%d, want 0:\n%s", args, code, out)
		}
		for _, name := range wantSubcommandNames {
			if !strings.Contains(out, name) {
				t.Errorf("ccpool %v output missing %q:\n%s", args, name, out)
			}
		}
	}
}

// TestEndToEnd_UnrecognizedSubcommandDoesNotSilentlyRunList reproduces the
// bug report's `ccpool send --help` case: an unrecognized first argument
// used to run the full session table (list) with the token passed through as
// list's own arg, instead of failing with usage help.
func TestEndToEnd_UnrecognizedSubcommandDoesNotSilentlyRunList(t *testing.T) {
	bin := buildCCPool(t)
	out, code := runHelpish(t, bin, "send", "alpha", "hi there")
	if code != 2 {
		t.Errorf("ccpool send ... exit=%d, want 2:\n%s", code, out)
	}
	if strings.Contains(out, "EXTERNAL_ID") {
		// list's own table header -- would indicate the old silent-fallback bug.
		t.Errorf("ccpool send ... ran list's table renderer instead of failing with usage:\n%s", out)
	}
	if !strings.Contains(out, "reply") {
		t.Errorf("ccpool send ... output does not surface the real subcommand list:\n%s", out)
	}
}
