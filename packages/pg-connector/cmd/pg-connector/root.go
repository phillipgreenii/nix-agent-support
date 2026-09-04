// root.go: pg-connector's root cobra command. pg-connector is the only
// user-facing CLI surface — a sibling packet's own pr verb group attaches
// its subcommands to this same root, as a new file inside this same
// cmd/pg-connector directory. pg-connector remains the single binary and
// the single user-facing CLI surface that pr (and, if built later,
// issue/ci/scm) are verb groups of, never a separate binary of its own.
// This packet does not itself add a pr subcommand.
package main

import "github.com/spf13/cobra"

// Version is set at build time by mkGoApp (versionPath = "main.Version").
var Version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pg-connector",
		Short:         "Unified pluggable connector umbrella CLI",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newAuthCmd())
	root.AddCommand(newConfigCmd())
	return root
}
