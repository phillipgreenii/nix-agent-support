package main

import "github.com/spf13/cobra"

// hideCmd is a stub: annotating a PR as user-hidden lands in a later packet
// of this docket. See docs/behavior/pg-desk/hide-unhide-wip.md.
var hideCmd = &cobra.Command{
	Use:   "hide",
	Short: "Hide a PR from the triage board (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("hide")
	},
}

func init() {
	rootCmd.AddCommand(hideCmd)
}
