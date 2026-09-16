package main

import "github.com/spf13/cobra"

// runCmd is a stub: the gather/interpret/(sync-schema-only) pipeline lands
// in a later packet of this docket. See docs/behavior/pg-desk/pipeline-run.md.
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the gather/interpret pipeline once (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("run")
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
