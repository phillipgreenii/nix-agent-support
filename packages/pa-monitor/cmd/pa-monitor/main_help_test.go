package main

import (
	"bytes"
	"strings"
	"testing"
)

// allSubcommandNames mirrors the top-level subcommands that WANT to appear in
// the help output. Kept local to the test so a drift between help text and
// this list is caught here.
var allSubcommandNames = []string{
	"daemon", "status", "agents-busy-check", "wait-until-agents-finished",
	"config", "caffeinate", "nudge", "info", "cmux-bridge", "auto-resume", "tui",
}

// TestPickSubcommandHelpFlags asserts -h/--help route to the pure "help"
// action rather than falling through to the TUI.
func TestPickSubcommandHelpFlags(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			cmd, _ := pickSubcommand([]string{"pa-monitor", flag})
			if cmd != "help" {
				t.Errorf("pickSubcommand(%q): got cmd %q, want %q", flag, cmd, "help")
			}
		})
	}
}

// TestPickSubcommandUnknownDoesNotFallThroughToTUI asserts an unknown non-flag
// arg is reported as itself (so main can emit an unknown-subcommand error)
// instead of silently launching the TUI.
func TestPickSubcommandUnknownDoesNotFallThroughToTUI(t *testing.T) {
	cmd, _ := pickSubcommand([]string{"pa-monitor", "list"})
	if cmd == "tui" {
		t.Fatalf("pickSubcommand(\"list\") fell through to TUI; want unknown-subcommand handling")
	}
	if cmd != "list" {
		t.Errorf("pickSubcommand(\"list\"): got cmd %q, want %q", cmd, "list")
	}
}

// TestRunHelpPrintsUsageAndExitsZero verifies the top-level --help output
// lists every subcommand and exits 0 on stdout (nothing on stderr).
func TestRunHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"pa-monitor", flag}, &stdout, &stderr)
			if code != 0 {
				t.Errorf("run(%q) exit code = %d, want 0", flag, code)
			}
			out := stdout.String()
			if !strings.Contains(strings.ToLower(out), "usage") {
				t.Errorf("run(%q) stdout missing usage header:\n%s", flag, out)
			}
			for _, name := range allSubcommandNames {
				if !strings.Contains(out, name) {
					t.Errorf("run(%q) stdout missing subcommand %q:\n%s", flag, name, out)
				}
			}
			if stderr.Len() != 0 {
				t.Errorf("run(%q) wrote to stderr: %q", flag, stderr.String())
			}
		})
	}
}

// TestRunUnknownSubcommandErrorsNonZero verifies an unknown arg produces an
// "unknown subcommand" error on stderr with a non-zero exit and does NOT
// launch the TUI or print usage to stdout.
func TestRunUnknownSubcommandErrorsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"pa-monitor", "list"}, &stdout, &stderr)
	if code == 0 {
		t.Errorf("run(\"list\") exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "unknown subcommand: list") {
		t.Errorf("run(\"list\") stderr = %q, want it to contain %q", stderr.String(), "unknown subcommand: list")
	}
	if stdout.Len() != 0 {
		t.Errorf("run(\"list\") wrote to stdout: %q", stdout.String())
	}
}
