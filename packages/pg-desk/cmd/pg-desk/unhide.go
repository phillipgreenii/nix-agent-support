package main

import "github.com/spf13/cobra"

// unhideCmd is a stub: clearing a PR's user-hidden annotation lands in a
// later packet of this docket. See docs/behavior/pg-desk/hide-unhide-wip.md.
var unhideCmd = &cobra.Command{
	Use:   "unhide",
	Short: "Unhide a PR on the triage board (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("unhide")
	},
}

func init() {
	rootCmd.AddCommand(unhideCmd)
}
