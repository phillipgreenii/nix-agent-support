package main

import (
	"os"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// TestMain lets the test binary stand in for the adapter: with
// PG_ROUTER_HANDLER_COMMAND_TEST_RUN set it runs the real run(os.Args[1:]) (the
// real subcommand switch, flag parsing and signal handling) instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("PG_ROUTER_HANDLER_COMMAND_TEST_RUN") == "1" {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestRun_SubcommandSet(t *testing.T) {
	if got := run(nil); got != conformance.ExitUsage {
		t.Errorf("no subcommand: exit %d, want %d", got, conformance.ExitUsage)
	}
	for _, ok := range []string{"help", "version"} {
		if got := run([]string{ok}); got != conformance.ExitOK {
			t.Errorf("%s: exit %d, want 0", ok, got)
		}
	}
	// The ccpool handler's own subcommands do not belong to this binary.
	for _, gone := range []string{"origin", "pool-capacity", "split", "query", "register", "self-status", "bogus"} {
		if got := run([]string{gone}); got != conformance.ExitUsage {
			t.Errorf("%s: exit %d, want %d (not a subcommand)", gone, got, conformance.ExitUsage)
		}
	}
	// Requests are JSON on stdin, never arguments.
	if got := run([]string{"validate", "stray"}); got != conformance.ExitUsage {
		t.Errorf("validate with an argument: exit %d, want %d", got, conformance.ExitUsage)
	}
}
