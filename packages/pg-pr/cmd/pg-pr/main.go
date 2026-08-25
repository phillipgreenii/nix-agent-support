package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// exitBusy is pg-pr's first app-specific exit code, per
// docs/adr/0042-coarse-exit-code-convention-busy-is-not-2.md's Decision table:
// 0 ok, 1 unexpected/generic error (MUST stay generic — never given a specific
// meaning, per this workspace's exit-code convention), 2 usage error, >=3
// app-specific. It is returned when a command gives up waiting on the
// cross-process per-PR merge-request-projection lock
// (internal/prlock.ErrTimeout) rather than proceeding — see `pr create`
// (cmd/pg-pr/pr_write.go's mergeRequestLock) and `pg-pr sync --pr/--repo`
// (which reaches the same give-up through internal/beadsbridge.Handler.Handle,
// captured by cmd/pg-pr/sync.go's dispatch wrapper). Per that ADR's 2026-08-24
// addendum, `9` is not a workspace-wide "busy" spelling — it is simply the
// value this app picked in the reserved >=3 band.
const exitBusy = 9

// exitCodeFor maps a command's returned error to a process exit code. Kept as
// a small pure function — rather than inlined in main — so the give-up-to-9
// mapping is unit-testable without invoking os.Exit.
func exitCodeFor(err error) int {
	if errors.Is(err, prlock.ErrTimeout) {
		return exitBusy
	}
	return 1
}

var rootCmd = &cobra.Command{
	Use:     "pg-pr",
	Short:   "Unified PR-work CLI for agents and humans",
	Version: Version,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "pg-pr %s\n", Version)
		return err
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Telemetry. Init never returns an error in practice — missing OTLP
	// endpoint installs a noop provider, bad endpoint logs a one-line
	// stderr warning and continues. The shutdown func flushes any
	// in-flight spans before the process exits.
	shutdown, _ := telemetry.Init(ctx, "pg-pr", Version)
	defer func() {
		shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
		defer cancelShutdown()
		_ = shutdown(shutdownCtx)
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
}
