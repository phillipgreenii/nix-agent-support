package main

import "github.com/spf13/cobra"

// openCmd is a stub: opening the configured browser on the current triage
// set lands in a later packet of this docket. See docs/behavior/pg-desk/open.md.
//
// This is the one subcommand the composition rule (D10) permits to exec a
// binary other than pg-connector: the operator-configured browser opener
// (open.chrome_bin). That exec is out of scope for this packet — the
// command body below is still just notImplemented — so no such call exists
// yet for composition_test.go's chokepoint test to reason about.
var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the configured browser on the current triage set (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("open")
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
}
