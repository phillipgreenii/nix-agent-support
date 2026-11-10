package main

import (
	"regexp"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/shutdown"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// The watchdog's exit code is documented in the exit table.
func TestExitTableDocumentsShutdownOverrun(t *testing.T) {
	if shutdown.ExitCode != wire.ExitShutdownOverrun || wire.ExitShutdownOverrun != 10 {
		t.Fatalf("exit code %d / %d, want 10", shutdown.ExitCode, wire.ExitShutdownOverrun)
	}
	if !regexp.MustCompile(`(?m)^  10 +serve hard-exited`).MatchString(exitTable) {
		t.Fatal("exitTable has no exit 10 row")
	}
}
