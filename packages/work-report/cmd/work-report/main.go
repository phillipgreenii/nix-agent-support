// Command work-report records and reports the operator's work activity, as
// surfaced by pg-connector's activity list. It execs the pg-connector binary
// from PATH and nothing else, and it MUST contain no organization identifiers.
//
// # Adding a subcommand
//
// This file wires only the root command and the registration seam. Each verb
// (pull, report, query, status, config and its children) lives in its own file
// of this package and registers itself from init():
//
//	func init() { registerCommand(newPullCmd) }
//
// registerCommand takes a constructor rather than a command so every
// newRootCmd call (the real main and each test) gets fresh command instances.
// A verb reads the resolved global flag values with globalFlags(cmd), which
// returns the raw --config, --output and --store values ("" when unset) and
// performs no defaulting: defaulting is owned by internal/config, and
// validation of the --output value is owned by each verb (pull additionally
// accepts "pg-router"). Neither registering nor reading global flags requires
// editing this file.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags (mkGoApp's default target).
var Version = "dev"

var (
	registryMu sync.Mutex
	registry   []func() *cobra.Command
)

// registerCommand adds a subcommand constructor to the root command tree. Verb
// files call it from init().
func registerCommand(newCmd func() *cobra.Command) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, newCmd)
}

// globalFlags returns the raw values of the root persistent flags --config,
// --output and --store as seen by cmd. A flag that was not passed yields "".
func globalFlags(cmd *cobra.Command) (configPath, output, storePath string) {
	get := func(name string) string {
		f := cmd.Flag(name)
		if f == nil {
			return ""
		}
		return f.Value.String()
	}
	return get("config"), get("output"), get("store")
}

// newRootCmd builds the root command with the global flags and every
// registered subcommand.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "work-report",
		Short:         "Record and report work activity surfaced through pg-connector",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Runnable so that --help lists usage and the global flags even before
		// any verb is registered; a bare invocation prints that help.
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	pf := root.PersistentFlags()
	pf.String("config", "", "path to the config file (default: $XDG_CONFIG_HOME/work-report/config.yaml)")
	pf.String("output", "", "output format: json or human")
	pf.String("store", "", "path to the store database (overrides the config's store path)")

	registryMu.Lock()
	ctors := append([]func() *cobra.Command(nil), registry...)
	registryMu.Unlock()
	for _, newCmd := range ctors {
		root.AddCommand(newCmd())
	}
	return root
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
