package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
)

// Version is set at build time via -ldflags.
var Version = "dev"

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
		os.Exit(1)
	}
}
