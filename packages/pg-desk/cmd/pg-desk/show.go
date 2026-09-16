package main

import "github.com/spf13/cobra"

// showCmd is a stub: printing a PR's stored interpretation lands in a later
// packet of this docket. See docs/behavior/pg-desk/operator-commands.md.
var showCmd = &cobra.Command{
	Use:   "show",
	Short: "Print a PR's stored interpretation (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("show")
	},
}

func init() {
	rootCmd.AddCommand(showCmd)
}
