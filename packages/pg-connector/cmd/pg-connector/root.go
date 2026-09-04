// root.go: pg-connector's root cobra command. pg-connector is the only
// user-facing CLI surface — the "generic pr entity/capability" and
// "generic ci entity/capability" packets' own pr/ci verb groups (pr.go and
// ci.go, in this same directory) attach their subcommands to this same
// root. pg-connector remains the single binary and the single user-facing
// CLI surface that pr/ci (and, if built later, issue/scm) are verb groups
// of, never a separate binary of its own.
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
	root.AddCommand(newPrCmd())
	root.AddCommand(newCiCmd())
	return root
}
