package main

import "github.com/spf13/cobra"

// wipCmd is a stub: marking a PR work-in-progress lands in a later packet of
// this docket. See docs/behavior/pg-desk/hide-unhide-wip.md.
var wipCmd = &cobra.Command{
	Use:   "wip",
	Short: "Mark a PR work-in-progress on the triage board (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("wip")
	},
}

func init() {
	rootCmd.AddCommand(wipCmd)
}
