package main

import "github.com/spf13/cobra"

// statusCmd is a stub: the store/health summary lands in a later packet of
// this docket. See docs/behavior/pg-desk/operator-commands.md.
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print the store path, schema version, and health summary (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("status")
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
