// root.go: pg-connector's root cobra command. pg-connector is the only
// user-facing CLI surface — each entity-type/capability packet's own verb
// group (pr.go's pr commands, ci.go's ci commands, issue.go's issue
// commands, scm.go's scm commands) attaches its subcommands to this same
// root. pg-connector remains the single binary and the single user-facing
// CLI surface that pr/ci/issue/scm are verb groups of, never a separate
// binary of its own.
//
// The persistent --output flag (registered here via output.go's
// addOutputFlag, so every verb group inherits it without redeclaring it
// itself) selects pg-connector's own CLI presentation mode
// [bead pg2-ox1k6] — see output.go's header comment for the json-default
// design rationale.
//
// root's PersistentPreRunE below is what makes --output validation
// genuinely pre-dispatch [bug A4, bead pg2-zc3b4]: cobra runs the nearest
// ancestor's PersistentPreRunE before the invoked (leaf) command's own
// RunE, and no verb-group command in this package defines its own
// PersistentPreRunE to shadow it — so this fires, for every verb, strictly
// before that verb's RunE calls Dispatch/fanOut*. Without it, an invalid
// --output value was only ever caught inside output.go's
// writeTargetedResult/writeFanOutResult, both of which run AFTER the
// backend has already executed (and, for a write op, already mutated
// state) — see output.go's own header on outputModeFor for the full
// story.
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
		// Validates --output before any subcommand's RunE runs, so a
		// bad value is caught with zero side effects — see this file's
		// header comment and output.go's outputModeFor.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			_, err := outputModeFor(cmd)
			return err
		},
	}
	root.AddCommand(newAuthCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newPrCmd())
	root.AddCommand(newCiCmd())
	root.AddCommand(newIssueCmd())
	root.AddCommand(newScmCmd())
	addOutputFlag(root)
	return root
}
