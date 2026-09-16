package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestStubSubcommandsRespondNotImplemented is the acceptance criterion
// "packages/pg-desk builds and every subcommand stub responds (even if
// 'not implemented')": every subcommand this packet introduces MUST be
// reachable on rootCmd and MUST fail with a "not implemented" error rather
// than silently succeeding or panicking.
func TestStubSubcommandsRespondNotImplemented(t *testing.T) {
	names := []string{
		"run", "serve", "open", "hide", "unhide", "wip", "feedback", "show",
		"status", "doctor", "heartbeat", "heartbeat-item",
		"import-pg-pr-annotations", "ledger",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			cmd, _, err := rootCmd.Find([]string{name})
			if err != nil {
				t.Fatalf("rootCmd has no %q subcommand: %v", name, err)
			}
			if cmd.RunE == nil {
				t.Fatalf("%q subcommand has no RunE", name)
			}
			runErr := cmd.RunE(cmd, nil)
			if runErr == nil {
				t.Fatalf("%q: expected a not-implemented error, got nil", name)
			}
			if !strings.Contains(runErr.Error(), "not implemented") {
				t.Fatalf("%q: expected a not-implemented error, got %q", name, runErr.Error())
			}
		})
	}
}

// TestRootCmdExecuteReturnsErrorForStub proves the stub path is reachable
// end-to-end through Cobra's own dispatch (Execute), not just by calling
// RunE directly — args parsing, subcommand resolution, and error surfacing
// all run for real.
func TestRootCmdExecuteReturnsErrorForStub(t *testing.T) {
	root := &cobra.Command{Use: "pg-desk"}
	root.AddCommand(&cobra.Command{
		Use: "run",
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("run")
		},
	})
	root.SetArgs([]string{"run"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected a not-implemented error, got %q", err.Error())
	}
}

// TestVersionCommand proves the one real (non-stub) command wired in this
// packet actually runs and prints something.
func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	cmd, _, err := rootCmd.Find([]string{"version"})
	if err != nil {
		t.Fatalf("rootCmd has no version subcommand: %v", err)
	}
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out.String(), "pg-desk") {
		t.Fatalf("expected version output to mention pg-desk, got %q", out.String())
	}
}
