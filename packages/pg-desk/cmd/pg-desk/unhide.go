package main

import "github.com/spf13/cobra"

// unhideCmd implements `pg-desk unhide <pr>`: clears the annotation table's
// hidden state [docs/behavior/pg-desk/hide-unhide-wip.md].
var unhideCmd = &cobra.Command{
	Use:   "unhide <pr>",
	Short: "Unhide a PR on the triage board",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setHiddenAnnotation(cmd, "unhide", args[0], false, "")
	},
}

func init() {
	rootCmd.AddCommand(unhideCmd)
}
