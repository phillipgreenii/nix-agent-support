package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestStubSubcommandsRespondNotImplemented is the acceptance criterion
// "packages/pg-desk builds and every subcommand stub responds (even if
// 'not implemented')": every subcommand THIS PHASE has not yet implemented
// MUST still be reachable on rootCmd and fail with a "not implemented"
// error rather than silently succeeding or panicking.
//
// "serve" and "import-pg-pr-annotations" are deliberately absent from this
// list: this docket's packet 7 (Phase 9) replaced serve's stub RunE with a
// real implementation (serve.go), so it no longer returns "not implemented"
// — it is instead covered by packages/pg-desk/internal/httpapi's own tests
// (the payload/metrics behavior) rather than a direct RunE call here, which
// would try to open the real default store and block forever on
// http.ListenAndServe. Likewise, packet 9 gave import-pg-pr-annotations a
// real implementation (see import_pg_pr_annotations.go), so it is covered by
// import_test.go instead. "run" is absent for the same reason as of packet
// 6: it now has a real implementation for <type>=pr (run.go,
// internal/pipeline), covered by run_test.go and internal/pipeline's own
// tests instead — only run issue/run thread still return "not
// implemented" (run_test.go's own TestRunCmdRejectsUnsupportedEntityType).
//
// This packet (8) gave open, hide, unhide, wip, feedback, show, status,
// doctor, heartbeat, and heartbeat-item real implementations too (see each
// command's own file and *_test.go), so they are removed from this list the
// same way. Only "ledger" remains a stub: it receives no writes until sync
// ships (Phase 10) — see docs/behavior/pg-desk/store-schema.md.
func TestStubSubcommandsRespondNotImplemented(t *testing.T) {
	names := []string{
		"ledger",
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
