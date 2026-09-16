package main

import "github.com/spf13/cobra"

// feedbackCmd is a stub: `feedback list`/`feedback set` land in a later
// packet of this docket. See docs/behavior/pg-desk/feedback.md.
var feedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "List or set an interpretation's feedback disposition (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("feedback")
	},
}

func init() {
	rootCmd.AddCommand(feedbackCmd)
}
