// Command pg-desk is the operator's triage desk for PR/issue work surfaced
// through pg-connector: it gathers facts, derives meaning from them, and
// serves a triage view and an opener, while signaling agent work through
// beads. It is generic and config-driven and MUST contain no organization
// identifiers — see docs/behavior/pg-desk/README.md.
//
// This file wires the Cobra command tree only. Every subcommand below is a
// STUB in this phase (docket pg2-2j5ac.32, Phase 9): each returns
// notImplemented (stub.go) until its own later packet in this docket fills
// it in with real behavior. Registration happens via each subcommand file's
// own init(), matching packages/pg-pr/cmd/pg-pr's existing pattern (see e.g.
// main.go there).
//
// Composition rule (D10, docs/behavior/pg-desk/README.md "Composition
// rule"): pg-desk execs no binary other than pg-connector and the
// operator-configured browser opener, directly or transitively — enforced
// by composition_test.go's chokepoint test.
//
// Telemetry (D24): nothing over OpenTelemetry/Prometheus in Phase 9 — see
// docs/behavior/pg-desk/README.md's "Telemetry declaration" section, already
// landed by this docket's first packet. main.go therefore does not wire a
// telemetry.Init the way packages/pg-pr/cmd/pg-pr/main.go does; that stays
// out of scope until an observability item calls for it.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:     "pg-desk",
	Short:   "Operator triage desk for PR/issue work surfaced through pg-connector",
	Version: Version,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "pg-desk %s\n", Version)
		return err
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
