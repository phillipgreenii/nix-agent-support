package main

import "github.com/spf13/cobra"

// doctorCmd is a stub: the config/pg-connector/serve health checks land in
// a later packet of this docket. See docs/behavior/pg-desk/operator-commands.md.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check config, pg-connector, and serve health (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("doctor")
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
